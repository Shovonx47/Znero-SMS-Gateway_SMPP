// Command api runs the Znero International HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/analytics"
	"github.com/znero-international/znero/backend/internal/app"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/auth"
	"github.com/znero-international/znero/backend/internal/billing"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/diagnostics"
	"github.com/znero-international/znero/backend/internal/health"
	"github.com/znero-international/znero/backend/internal/httpprovider"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/metrics"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/internal/smpp"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
	"github.com/znero-international/znero/backend/internal/smppserver"
	"github.com/znero-international/znero/backend/internal/system"
	"github.com/znero-international/znero/backend/internal/webhook"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/kafkaclient"
	"github.com/znero-international/znero/backend/pkg/logx"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func isLoopbackBindAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func run() error {
	roleFlag := flag.String("role", "", "Process role: all|api|smpp|dispatcher|reaper|webhook|poller|analytics (or ZNERO_ROLE)")
	pprofFlag := flag.String("pprof-addr", "", "Optional loopback-only pprof bind address (e.g. 127.0.0.1:6060)")
	flag.Parse()

	role := strings.ToLower(strings.TrimSpace(*roleFlag))
	if role == "" {
		role = strings.ToLower(strings.TrimSpace(os.Getenv("ZNERO_ROLE")))
	}
	if role == "" {
		role = "all"
	}
	switch role {
	case "all", "api", "smpp", "dispatcher", "reaper", "webhook", "poller", "analytics":
	default:
		return fmt.Errorf("invalid -role %q (allowed: all, api, smpp, dispatcher, reaper, webhook, poller, analytics)", role)
	}

	pprofAddr := strings.TrimSpace(*pprofFlag)
	if pprofAddr == "" {
		pprofAddr = strings.TrimSpace(os.Getenv("ZNERO_PPROF_ADDR"))
	}
	if pprofAddr != "" && !isLoopbackBindAddr(pprofAddr) {
		return fmt.Errorf("security violation: -pprof-addr %q must bind strictly to loopback", pprofAddr)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	asyncOut := logx.NewAsyncWriter(os.Stdout, 16384)
	defer asyncOut.Close()
	logger, err := logx.New(asyncOut, cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	logger.Info("starting znero backend", slog.String("version", app.Version), slog.Any("config", cfg))
	logger.Info("starting znero backend", slog.String("version", app.Version), slog.String("role", role), slog.Any("config", cfg))
	metrics.DefaultRegistry.SetRole(role)
	http.Handle("/metrics", metrics.DefaultRegistry.HTTPHandler())

	// ctx is cancelled on Ctrl+C / SIGTERM and drives graceful shutdown of everything below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var pprofSrv *http.Server
	if pprofAddr != "" {
		pprofSrv = &http.Server{
			Addr:              pprofAddr,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			logger.Info("pprof listening (loopback-only)", slog.String("addr", pprofAddr))
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("pprof server stopped", slog.String("error", err.Error()))
			}
		}()
		defer func() {
			sCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = pprofSrv.Shutdown(sCtx)
		}()
	}

	// PostgreSQL is required: fail fast (with a password-free error) if it is unreachable.
	pool, err := database.Connect(ctx, database.Config{
		URL:               cfg.Database.URL,
		MaxConns:          cfg.Database.MaxConns,
		MinConns:          cfg.Database.MinConns,
		MaxConnLifetime:   cfg.Database.MaxConnLifetime,
		MaxConnIdleTime:   cfg.Database.MaxConnIdleTime,
		HealthCheckPeriod: cfg.Database.HealthCheckPeriod,
		ConnectTimeout:    cfg.Database.ConnectTimeout,
	})
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	// Startup safety check: ensure development seed accounts and legacy test accounts never exist in non-development environments
	if !cfg.IsDevelopment() {
		var devAccountCount int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM users
			WHERE email LIKE '%@dev.znero.test'
			   OR email LIKE '%@example.com%'
			   OR email LIKE '%@znero.internal%'
			   OR email LIKE '%@apextelecom.com%'
			   OR email LIKE '%@bluestar-sms.com%'
			   OR email LIKE '%@fintechcorp.com%'
		`).Scan(&devAccountCount)
		if err == nil && devAccountCount > 0 {
			logger.Warn("CRITICAL SECURITY ALERT: DEVELOPMENT / LEGACY TEST ACCOUNTS DETECTED IN NON-DEVELOPMENT ENVIRONMENT! PURGE THEM IMMEDIATELY.",
				slog.Int("dev_account_count", devAccountCount),
				slog.String("env", cfg.Env),
			)
		}
	}

	// Kafka is optional at runtime: creating the clients never contacts the brokers, and they
	// reconnect with backoff, so a broker outage only turns /readyz's kafka check to "down".
	kcfg := kafkaclient.Config{
		Brokers:        cfg.Kafka.Brokers,
		ClientID:       cfg.Kafka.ClientID,
		DialTimeout:    cfg.Kafka.DialTimeout,
		RequestTimeout: cfg.Kafka.RequestTimeout,
	}
	kc, err := kafkaclient.New(kcfg, logger)
	if err != nil {
		return err
	}
	defer kc.Close()

	healthSvc := health.NewService(app.Version, cfg.ReadyCheckTimeout, logger,
		health.NewProbe("database", pool),
		health.NewProbe("kafka", kc),
	)

	// Phase 2: Audit, RBAC, Tenancy (Org), and Authentication
	auditRepo := audit.NewRepository(pool)
	auditSvc := audit.NewService(auditRepo)
	auditHandler := audit.NewHandler(auditSvc)

	rbacRepo := rbac.NewRepository(pool)
	rbacSvc := rbac.NewService(rbacRepo, auditSvc, pool)
	rbacHandler := rbac.NewHandler(rbacSvc)

	orgRepo := org.NewRepository(pool)
	orgSvc := org.NewService(orgRepo, rbacRepo, auditSvc, pool)
	orgHandler := org.NewHandler(orgSvc)

	tokenMgr, err := tokens.NewTokenManager(cfg.Auth.JWTAccessSecret, cfg.Auth.JWTAccessDuration)
	if err != nil {
		return fmt.Errorf("token manager: %w", err)
	}

	var mailer auth.Mailer = auth.NewDevConsoleMailer(cfg.Env, logger)
	authRepo := auth.NewRepository(pool)
	authSvc := auth.NewService(authRepo, orgRepo, rbacRepo, auditSvc, tokenMgr, mailer, pool, cfg.Auth.MFAEncryptionKey, cfg.Env)
	authHandler := auth.NewHandler(authSvc, cfg)
	authMiddleware := auth.Authenticate(tokenMgr, authRepo, orgRepo)

	// Phase 3: Master Data and Number Intelligence
	approvalRepo := approval.NewRepository(pool)
	approvalSvc := approval.NewService(approvalRepo, auditSvc, pool)
	approvalHandler := approval.NewHandler(approvalSvc)

	countryRepo := country.NewRepository(pool)
	countrySvc := country.NewService(countryRepo, approvalSvc, auditSvc, pool)
	countryHandler := country.NewHandler(countrySvc)

	operatorRepo := operator.NewRepository(pool)
	operatorSvc := operator.NewService(operatorRepo, approvalSvc, auditSvc, pool)
	operatorHandler := operator.NewHandler(operatorSvc)

	prefixRepo := prefix.NewRepository(pool)
	prefixSvc := prefix.NewService(prefixRepo, approvalSvc, auditSvc, pool)
	prefixHandler := prefix.NewHandler(prefixSvc)

	importRepo := importjob.NewRepository(pool)
	importSvc := importjob.NewService(importRepo, countryRepo, operatorRepo, prefixRepo, prefixSvc, approvalSvc, auditSvc, pool, "")
	importHandler := importjob.NewHandler(importSvc)

	numberRepo := number.NewRepository(pool)
	numberSvc := number.NewService(numberRepo, prefixRepo, prefixSvc, approvalSvc, auditSvc, pool, logger)
	numberHandler := number.NewHandler(numberSvc)

	// Phase 4: Providers, Provider Coverage, Customer Commercial Profiles, Credentials, and Sender ID Registry
	keyRing, err := secrets.NewKeyRingFromEnv(
		cfg.Env,
		os.Getenv("SECRETS_ENCRYPTION_KEY"),
		os.Getenv("SECRETS_KEY_ID"),
		os.Getenv("SECRETS_PREVIOUS_KEYS"),
	)
	if err != nil {
		return fmt.Errorf("secrets keyring: %w", err)
	}

	providerRepo := provider.NewRepository(pool)
	providerSvc := provider.NewService(providerRepo, auditSvc, approvalSvc, keyRing, cfg.Env)
	providerSvc.SetAllowLoopback(cfg.AllowLoopback())
	providerHandler := provider.NewHandler(providerSvc)

	customerRepo := customer.NewRepository(pool)
	customerSvc := customer.NewService(customerRepo, auditSvc, approvalSvc)
	// Load platform global defaults for new customer billing terms
	globBillingMode := "on_delivery"
	var settingRaw string
	if err := pool.QueryRow(ctx, `SELECT trim(both '"' FROM value::text) FROM system_settings WHERE key = 'billing.sms.customer_trigger'`).Scan(&settingRaw); err == nil {
		if settingRaw == "on_provider_accept" || settingRaw == "on_delivery" {
			globBillingMode = settingRaw
		}
	}
	globNoDLRPolicy := strings.TrimSpace(os.Getenv("MESSAGING_NO_DLR_HOLD_POLICY"))
	if globNoDLRPolicy == "" {
		globNoDLRPolicy = strings.TrimSpace(os.Getenv("NO_DLR_HOLD_POLICY"))
	}
	if globNoDLRPolicy != "retain_until_resolution" && globNoDLRPolicy != "immediate_release" && globNoDLRPolicy != "capture_at_ttl" {
		globNoDLRPolicy = "retain_until_resolution"
	}
	customerSvc.SetBillingTermDefaults(customer.BillingTermDefaults{
		BillingMode:             globBillingMode,
		DLRWaitHours:            24,
		NoDLRPolicy:             globNoDLRPolicy,
		FailedAfterAcceptPolicy: "keep_charge",
	})
	customerHandler := customer.NewHandler(customerSvc)

	senderIDRepo := senderid.NewRepository(pool)
	senderIDSvc := senderid.NewService(senderIDRepo, auditSvc, approvalSvc)
	senderIDHandler := senderid.NewHandler(senderIDSvc)

	ratingRepo := rating.NewRepository(pool)
	ratingEngine := rating.NewInMemoryEngine(ratingRepo)
	_ = ratingEngine.Reload(ctx)
	ratingSvc := rating.NewService(ratingRepo, auditSvc, approvalSvc, numberSvc, ratingEngine)
	ratingHandler := rating.NewHandler(ratingSvc)

	ledgerRepo := ledger.NewRepository(pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, approvalSvc, auditSvc, pool)
	ledgerHandler := ledger.NewHandler(ledgerSvc)

	billingRepo := billing.NewRepository(pool)
	billingSvc := billing.NewService(billingRepo, orgRepo, ledgerRepo, approvalSvc, auditSvc, pool)
	billingHandler := billing.NewHandler(billingSvc)

	routingRepo := routing.NewRepository(pool)
	raterAdapter := routing.NewRatingEngineAdapter(ratingEngine)
	numberAdapter := routing.NewNumberServiceAdapter(numberSvc)
	routingEngine := routing.NewEngine(pool, raterAdapter, numberAdapter, nil)
	routingSvc := routing.NewService(routingRepo, approvalSvc, auditSvc, pool, routingEngine)
	routingHandler := routing.NewHandler(routingSvc)
	routing.TryReload(ctx, pool)

	importSvc.SetPhase4Repositories(providerRepo, senderIDRepo)
	importSvc.SetRatingServices(ratingRepo, ratingSvc)

	// Phase 8: Messaging Engine & SMPP Gateway Wiring
	msgRepo := messaging.NewRepository(pool)
	_ = msgRepo.EnsureMonthlyPartitions(ctx, 3)

	var dlrProcRef *messaging.DLRProcessor
	smppMgr := smpp.NewConnectionManager(smpp.ManagerConfig{
		DB:            pool,
		Decrypter:     keyRing,
		AllowLoopback: cfg.AllowLoopback(),
		Logger:        logger,
		DLRHandler: func(dCtx context.Context, connectionID uuid.UUID, deliver *pdu.DeliverSM) error {
			if dlrProcRef != nil {
				return dlrProcRef.ProcessDeliverSM(dCtx, connectionID, deliver)
			}
			return nil
		},
	})
	runHTTP := role == "all" || role == "api"
	runSMPP := role == "all" || role == "smpp"
	runDispatcher := role == "all" || role == "dispatcher"
	runReaper := role == "all" || role == "reaper"
	runWebhook := role == "all" || role == "webhook"
	runPoller := role == "all" || role == "poller"
	runAnalytics := role == "all" || role == "analytics"

	if runDispatcher {
		smppMgr.StartMetricsPersister(ctx, 5*time.Second)
		defer func() { _ = smppMgr.Close() }()
	}

	smppClient := smpp.NewClient(smpp.ClientConfig{
		Manager: smppMgr,
		DB:      pool,
		Logger:  logger,
	})
	httpProvClient := httpprovider.NewClient(httpprovider.ClientConfig{
		Pool:          pool,
		Decrypter:     keyRing,
		AllowLoopback: cfg.AllowLoopback(),
		CacheTTL:      5 * time.Second,
		Logger:        logger,
	})
	providerSvc.SetHTTPTestSender(httpProvClient)
	compositeSender := messaging.NewCompositeSender(pool, map[string]messaging.Sender{
		"smpp": smppClient,
		"http": httpProvClient,
	}, 5*time.Second)

	msgCfg := messaging.DefaultConfig()
	msgCfg.DefaultBillingTerms = messaging.CustomerBillingTerms{
		BillingMode:             globBillingMode,
		DLRWaitHours:            24,
		NoDLRPolicy:             globNoDLRPolicy,
		FailedAfterAcceptPolicy: "keep_charge",
	}
	msgSvc, err := messaging.NewService(pool, msgRepo, msgCfg, routingEngine, ledgerSvc, keyRing, auditSvc, compositeSender, logger)
	if err != nil {
		return fmt.Errorf("messaging service: %w", err)
	}

	dlrProc := messaging.NewDLRProcessor(msgRepo, ledgerSvc, globBillingMode, logger)
	dlrProcRef = dlrProc
	httpCallbackHandler := httpprovider.NewCallbackHandler(httpProvClient, dlrProc, logger)

	reconcileSvc := messaging.NewReconciliationService(pool, msgRepo, ledgerSvc, logger)
	messagingHandler := messaging.NewHandler(msgSvc, reconcileSvc, &smppStatsAdapter{mgr: smppMgr}, msgRepo)

	var dispatcher *messaging.Dispatcher
	if runDispatcher {
		dispatcher = messaging.NewDispatcher(pool, msgRepo, routingEngine, ledgerSvc, compositeSender, logger)
		dispatcher.Start(ctx)
	}

	var httpDLRPoller *httpprovider.DLRPoller
	if runPoller {
		httpDLRPoller = httpprovider.NewDLRPoller(pool, httpProvClient, dlrProc, httpprovider.PollerConfig{}, logger)
		httpDLRPoller.Start(ctx)
	}

	var reaper *messaging.Reaper
	if runReaper {
		reaperPoll := 30 * time.Second
		if cfg.Env == "development" || cfg.Env == "test" {
			reaperPoll = 2 * time.Second
		}
		reaper = messaging.NewReaper(pool, msgRepo, ledgerSvc, dlrProc, messaging.ReaperConfig{
			PollInterval:    reaperPoll,
			NoDLRHoldPolicy: messaging.NoDLRHoldPolicy(globNoDLRPolicy),
		}, logger)
		reaper.Start(ctx)
	}

	// Phase 8B: Customer Edge, SMPP Server, Webhook Dispatcher & Inbound Pipeline
	edgeAuth := customeredge.NewEdgeAuthenticator(pool, nil, logger)
	edgeLimiter := customeredge.NewCustomerLimiter()
	edgeHandler := customeredge.NewEdgeHandler(edgeAuth, edgeLimiter, msgSvc, msgRepo, ledgerSvc, ratingSvc, keyRing, pool)
	edgeHandler.SetAllowLoopback(string(cfg.Env), cfg.AllowLoopback())

	var wg sync.WaitGroup
	var smppServer *smppserver.Server
	var customerDLRDispatcher *smppserver.DLRDispatcher
	var smppErrCh chan error

	smppAddr := os.Getenv("CUSTOMER_SMPP_ADDR")
	if smppAddr == "" {
		smppAddr = net.JoinHostPort(cfg.Server.Host, "2776")
	}
	if runSMPP {
		smppServer = smppserver.NewServer(smppserver.ServerConfig{
			Addr:            smppAddr,
			SystemID:        "ZNERO",
			IdleTimeout:     60 * time.Second,
			EnquireInterval: 30 * time.Second,
			DefaultWindow:   16,
		}, pool, msgSvc, msgRepo, edgeLimiter, logger)
		messagingHandler.SetCustomerSessionProvider(smppServer)

		smppErrCh = make(chan error, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := smppServer.Start(ctx); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Error("customer smpp server stopped with error", slog.String("error", err.Error()))
				smppErrCh <- err
			}
		}()

		customerDLRDispatcher = smppserver.NewDLRDispatcher(smppserver.DLRDispatcherConfig{
			PollInterval: 50 * time.Millisecond,
			BatchSize:    200,
		}, pool, msgRepo, smppServer.Tracker(), logger)
		customerDLRDispatcher.Start(ctx)

		inboundHandler := messaging.NewInboundHandler(msgRepo, keyRing, smppServer, logger)
		dlrProc.SetInboundHandler(inboundHandler)
	} else {
		inboundHandler := messaging.NewInboundHandler(msgRepo, keyRing, nil, logger)
		dlrProc.SetInboundHandler(inboundHandler)
	}

	var whDispatcher *webhook.Dispatcher
	if runWebhook {
		whDispatcher = webhook.NewDispatcher(msgRepo, keyRing, webhook.DefaultConfig(cfg.AllowLoopback()), logger)
		whDispatcher.Start(ctx)
	}

	analyticsRepo := analytics.NewRepository(pool, logger)
	analyticsSvc := analytics.NewService(analyticsRepo, logger)
	analyticsHandler := analytics.NewHandler(analyticsSvc)

	var analyticsWorker *analytics.Worker
	if runAnalytics {
		workerCfg := analytics.DefaultWorkerConfig()
		if cfg.IsDevelopment() || cfg.Env == config.EnvTest {
			workerCfg.PollInterval = 500 * time.Millisecond
		}
		analyticsWorker = analytics.NewWorker(pool, analyticsRepo, workerCfg, logger)
		analyticsWorker.Start(ctx)
	}

	// Keep routing/rating caches synchronized across multi-process deployments.
	if runHTTP || runSMPP || runDispatcher {
		reloadInterval := 60 * time.Second
		if cfg.IsDevelopment() || cfg.Env == config.EnvTest {
			reloadInterval = 5 * time.Second
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(reloadInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					routing.TryReload(ctx, pool)
					_ = ratingEngine.Reload(ctx)
				}
			}
		}()
	}

	if runHTTP {
		modules := app.Modules{
			Health:               health.NewHandler(healthSvc),
			System:               system.NewHandler(system.NewService(system.NewRepository(pool), app.Version, cfg.Env)),
			Auth:                 authHandler,
			Org:                  orgHandler,
			RBAC:                 rbacHandler,
			Audit:                auditHandler,
			Country:              countryHandler,
			Operator:             operatorHandler,
			Prefix:               prefixHandler,
			Approval:             approvalHandler,
			ImportJob:            importHandler,
			Number:               numberHandler,
			Provider:             providerHandler,
			Customer:             customerHandler,
			SenderID:             senderIDHandler,
			Rating:               ratingHandler,
			Ledger:               ledgerHandler,
			Billing:              billingHandler,
			Routing:              routingHandler,
			Messaging:            messagingHandler,
			CustomerEdge:         edgeHandler,
			HTTPProviderCallback: httpCallbackHandler,
			Analytics:            analyticsHandler,
			AuthMiddleware:       authMiddleware,
		}
		diagSvc := diagnostics.NewService(kc, cfg.Kafka.TestTopic, cfg.Kafka.RequestTimeout, logger)
		if cfg.IsDevelopment() {
			modules.Diagnostics = diagnostics.NewHandler(diagSvc)
		}

		if cfg.IsDevelopment() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ensureTopic(ctx, kc, cfg.Kafka.TestTopic, logger)
			}()
		}
		if cfg.Kafka.ConsumerEnabled {
			consumer, err := kafkaclient.NewConsumer(kcfg, cfg.Kafka.ConsumerGroup, cfg.Kafka.TestTopic, diagSvc.LogMessage, logger)
			if err != nil {
				return err
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer consumer.Close()
				consumer.Run(ctx)
			}()
		}

		server := app.NewHTTP(cfg, logger, modules)
		listenErr := make(chan error, 1)
		go func() {
			logger.Info("http server listening", slog.String("addr", cfg.Server.Addr()), slog.String("role", role))
			listenErr <- server.Listen(cfg.Server.Addr())
		}()

		select {
		case err := <-listenErr:
			stop()
			wg.Wait()
			return fmt.Errorf("http server: %w", err)
		case <-ctx.Done():
			logger.Info("shutdown signal received; draining", slog.String("role", role))
		}

		if err := server.ShutdownWithTimeout(cfg.Server.ShutdownTimeout); err != nil {
			logger.Error("http shutdown did not complete cleanly", slog.String("error", err.Error()))
		}
	} else {
		logger.Info("component process running", slog.String("role", role), slog.String("smpp_addr", smppAddr))
		if smppErrCh != nil {
			select {
			case err := <-smppErrCh:
				stop()
				return fmt.Errorf("smpp server: %w", err)
			case <-ctx.Done():
				logger.Info("shutdown signal received; draining", slog.String("role", role))
			}
		} else {
			<-ctx.Done()
			logger.Info("shutdown signal received; draining", slog.String("role", role))
		}
	}

	if dispatcher != nil {
		dispatcher.Stop()
	}
	if httpDLRPoller != nil {
		httpDLRPoller.Stop()
	}
	if reaper != nil {
		reaper.Stop()
	}
	if customerDLRDispatcher != nil {
		customerDLRDispatcher.Stop()
	}
	if whDispatcher != nil {
		whDispatcher.Stop()
	}
	if analyticsWorker != nil {
		analyticsWorker.Stop()
	}
	if smppServer != nil {
		smppServer.Stop()
	}
	if !waitTimeout(&wg, cfg.Server.ShutdownTimeout) {
		logger.Warn("background workers did not stop in time")
	}
	logger.Info("shutdown complete", slog.String("role", role))
	return nil
}

// ensureTopic creates the Kafka test topic, retrying with backoff until it succeeds or ctx ends.
func ensureTopic(ctx context.Context, kc *kafkaclient.Client, topic string, logger *slog.Logger) {
	delay := 2 * time.Second
	for ctx.Err() == nil {
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := kc.EnsureTopic(tctx, topic, 1, 1)
		cancel()
		if err == nil {
			logger.Info("kafka test topic ready", slog.String("topic", topic))
			return
		}
		if !errors.Is(err, context.Canceled) {
			logger.Warn("kafka test topic not ready yet (will retry)", slog.String("topic", topic), slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

type smppStatsAdapter struct {
	mgr *smpp.ConnectionManager
}

func (a *smppStatsAdapter) GetAllSessionStats(ctx context.Context) ([]messaging.SessionStats, error) {
	stats, err := a.mgr.GetAllSessionStats(ctx)
	if err != nil {
		return nil, err
	}
	res := make([]messaging.SessionStats, len(stats))
	for i, s := range stats {
		res[i] = messaging.SessionStats{
			ConnectionID:   s.ConnectionID,
			Bound:          s.Bound,
			InFlight:       s.InFlight,
			CurrentTPS:     s.CurrentTPS,
			SuccessCount:   s.SuccessCount,
			FailureCount:   s.FailureCount,
			LastErrorCode:  s.LastErrorCode,
			ReconnectCount: s.ReconnectCount,
		}
	}
	return res, nil
}
