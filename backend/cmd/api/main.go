// Command api runs the Znero International HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/znero-international/znero/backend/internal/app"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/auth"
	"github.com/znero-international/znero/backend/internal/billing"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/diagnostics"
	"github.com/znero-international/znero/backend/internal/health"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/internal/system"
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

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := logx.New(os.Stdout, cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	logger.Info("starting znero backend", slog.String("version", app.Version), slog.Any("config", cfg))

	// ctx is cancelled on Ctrl+C / SIGTERM and drives graceful shutdown of everything below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	providerHandler := provider.NewHandler(providerSvc)

	customerRepo := customer.NewRepository(pool)
	customerSvc := customer.NewService(customerRepo, auditSvc, approvalSvc)
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

	modules := app.Modules{
		Health:         health.NewHandler(healthSvc),
		System:         system.NewHandler(system.NewService(system.NewRepository(pool), app.Version, cfg.Env)),
		Auth:           authHandler,
		Org:            orgHandler,
		RBAC:           rbacHandler,
		Audit:          auditHandler,
		Country:        countryHandler,
		Operator:       operatorHandler,
		Prefix:         prefixHandler,
		Approval:       approvalHandler,
		ImportJob:      importHandler,
		Number:         numberHandler,
		Provider:       providerHandler,
		Customer:       customerHandler,
		SenderID:       senderIDHandler,
		Rating:         ratingHandler,
		Ledger:         ledgerHandler,
		Billing:        billingHandler,
		Routing:        routingHandler,
		AuthMiddleware: authMiddleware,
	}
	diagSvc := diagnostics.NewService(kc, cfg.Kafka.TestTopic, cfg.Kafka.RequestTimeout, logger)
	if cfg.IsDevelopment() {
		modules.Diagnostics = diagnostics.NewHandler(diagSvc)
	}

	// Background Kafka work (topic creation + logging consumer).
	var wg sync.WaitGroup
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

	// Background routing snapshot periodic refresh (every 60s)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				routing.TryReload(ctx, pool)
			}
		}
	}()

	server := app.NewHTTP(cfg, logger, modules)
	listenErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", cfg.Server.Addr()))
		listenErr <- server.Listen(cfg.Server.Addr())
	}()

	select {
	case err := <-listenErr:
		stop()
		wg.Wait()
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received; draining")
	}

	// Graceful shutdown: stop accepting requests and drain, then stop background workers, then
	// (deferred) close Kafka and the database pool.
	if err := server.ShutdownWithTimeout(cfg.Server.ShutdownTimeout); err != nil {
		logger.Error("http shutdown did not complete cleanly", slog.String("error", err.Error()))
	}
	if !waitTimeout(&wg, cfg.Server.ShutdownTimeout) {
		logger.Warn("background workers did not stop in time")
	}
	logger.Info("shutdown complete")
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
