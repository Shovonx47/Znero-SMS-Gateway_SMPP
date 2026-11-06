import { test, expect } from '@playwright/test';

test.describe('Phase 6: Wallets, Ledger, Credit Enforcement & Billing Basics E2E', () => {
	const adminEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
	const makerEmail = process.env.TEST_MAKER_EMAIL || 'dev-maker@dev.znero.test';
	const checkerEmail = process.env.TEST_CHECKER_EMAIL || 'dev-checker@dev.znero.test';
	const resellerEmail = process.env.TEST_RESELLER_EMAIL || 'dev-reseller-admin@dev.znero.test';
	const customerEmail = process.env.TEST_CUSTOMER_EMAIL || 'dev-customer-admin@dev.znero.test';
	const password = process.env.TEST_ADMIN_PASSWORD || 'DevSeed1234!#';

	async function loginAs(page: import('@playwright/test').Page, email: string) {
		await page.goto('/login');
		await page.fill('input#email', email);
		await page.fill('input#password', password);
		await page.click('button[type="submit"]');
		await page.waitForURL('**/dashboard', { timeout: 15000 });
	}

	test('1 & 2. Maker submits top-up -> Dual-Control: self-approval refused -> Checker approves -> Balance updates', async ({
		browser
	}) => {
		// --- 1. Maker logs in and requests a wallet top-up ---
		const makerContext = await browser.newContext();
		const makerPage = await makerContext.newPage();
		await loginAs(makerPage, makerEmail);

		await makerPage.goto('/wallets');
		await expect(makerPage.locator('main h1')).toContainText('Wallets');

		// Click into first active wallet
		const firstWalletLink = makerPage.locator('tbody tr:has-text("active") a[href*="/wallets/"]').first();
		await expect(firstWalletLink).toBeVisible({ timeout: 10000 });
		const walletHref = (await firstWalletLink.getAttribute('href')) || '';
		expect(walletHref).toContain('/wallets/');
		await firstWalletLink.click();
		await makerPage.waitForURL(/\/wallets\/[a-f0-9-]+/, { timeout: 15000 });

		// Click "+ Top-up"
		await makerPage.locator('button:has-text("+ Top-up")').click();
		await expect(makerPage.locator('h2:has-text("Manual Wallet Top-up")')).toBeVisible();

		// Fill top-up form
		const topupAmt = '350.0000';
		const topupReason = 'Playwright dual-control maker-checker E2E test';
		await makerPage.fill('input#topup-amount', topupAmt);
		await makerPage.fill('textarea#topup-reason', topupReason);
		await makerPage.fill('input#topup-ref', 'E2E-MC-WIRE-99');
		await makerPage.locator('button:has-text("Confirm Top-up")').click();

		// Verify dual-control pending approval notice in modal
		await expect(
			makerPage.locator('text=Top-up request submitted for Dual-Control Maker-Checker approval')
		).toBeVisible({ timeout: 10000 });

		// --- 2. Dual-control enforcement: Maker visits Pending Changes and cannot self-approve ---
		await makerPage.goto('/master-data/pending-changes');
		await expect(makerPage.locator('main h1')).toContainText('Pending Changes');

		// Filter by entity type: wallet_topup (select index 1)
		const entityFilter = makerPage.locator('select').nth(1);
		if (await entityFilter.isVisible()) {
			await entityFilter.selectOption('wallet_topup');
			await makerPage.waitForTimeout(500);
		}

		// Maker sees their own request marked with "YOU" badge
		const ownRow = makerPage.locator('tr', { hasText: 'wallet_topup' }).first();
		await expect(ownRow).toBeVisible({ timeout: 10000 });
		await expect(ownRow).toContainText('YOU');

		// Maker cannot approve own request: Approve button is omitted for maker on this row
		await expect(ownRow.locator('button:has-text("Approve")')).toHaveCount(0);

		// Maker tries direct API call to approve own request -> MUST be refused with 400 or 403
		const apiCheckResult = await makerPage.evaluate(async () => {
			const refreshRes = await fetch('/api/v1/auth/refresh', { method: 'POST', credentials: 'include' });
			const refreshJson = await refreshRes.json();
			const token = refreshJson?.data?.access_token || '';

			const res = await fetch('/api/v1/change-requests?status=pending&entity_type=wallet_topup', {
				headers: { Authorization: `Bearer ${token}` }
			});
			const json = await res.json();
			const items = json.data?.items || json.data || [];
			if (!items || items.length === 0) return { status: 0, error: 'no pending' };
			const reqId = items[0].id;
			const approveRes = await fetch(`/api/v1/change-requests/${reqId}/approve`, {
				method: 'POST',
				headers: {
					Authorization: `Bearer ${token}`,
					'Content-Type': 'application/json'
				},
				body: JSON.stringify({ notes: 'Maker illegal self-approval attempt' })
			});
			return { status: approveRes.status };
		});
		expect([400, 403]).toContain(apiCheckResult.status);

		await makerContext.close();

		// --- 3. Checker logs in and approves Maker's top-up request ---
		const checkerContext = await browser.newContext();
		const checkerPage = await checkerContext.newPage();
		await loginAs(checkerPage, checkerEmail);

		await checkerPage.goto('/master-data/pending-changes');
		await expect(checkerPage.locator('main h1')).toContainText('Pending Changes');

		// Auto-accept confirmation dialogs
		checkerPage.on('dialog', (dialog) => dialog.accept());

		// Find pending wallet_topup change request
		const topupRow = checkerPage.locator('tr', { hasText: 'wallet_topup' }).first();
		await expect(topupRow).toBeVisible({ timeout: 10000 });

		// Click active Approve button
		const approveBtn = topupRow.locator('button:has-text("Approve"):not([disabled])');
		await expect(approveBtn).toBeVisible();
		await approveBtn.click();

		// Row should disappear or update
		await checkerPage.waitForTimeout(1000);

		// --- 4. Verify balance and ledger entry on wallet page ---
		await checkerPage.goto(walletHref);
		await expect(checkerPage.locator('main h1')).toBeVisible();

		// Check Ledger tab for the posted top-up transaction
		await checkerPage.locator('button:has-text("Ledger Transactions")').click();
		await expect(checkerPage.locator('table')).toContainText('topup', { timeout: 10000 });
		await expect(checkerPage.locator('table')).toContainText(topupReason);

		await checkerContext.close();
	});

	test('3. Customer user isolation: views only own wallet & invoices; platform endpoints & pages are 403 / hidden', async ({
		page
	}) => {
		await loginAs(page, customerEmail);

		// 1. Sidebar checks: Platform-only navigation items must NOT be present
		await expect(page.locator('aside a[href="/providers"]')).toHaveCount(0);
		await expect(page.locator('aside a[href="/rates/buy-cards"]')).toHaveCount(0);
		await expect(page.locator('aside a[href="/reconciliation"]')).toHaveCount(0);

		// 2. Direct page access to platform-only page (Reconciliation) should be protected / forbidden
		await page.goto('/reconciliation');
		await expect(
			page.locator('text=Access Denied').or(page.locator('text=403')).or(page.locator('text=Forbidden')).or(page.locator('main h1:not(:has-text("Reconciliation"))'))
		).toBeVisible({ timeout: 10000 });

		// 3. Direct API calls to platform endpoints must return 403 Forbidden
		const forbiddenChecks = await page.evaluate(async () => {
			const refreshRes = await fetch('/api/v1/auth/refresh', { method: 'POST', credentials: 'include' });
			const refreshJson = await refreshRes.json();
			const token = refreshJson?.data?.access_token || '';
			const headers = { Authorization: `Bearer ${token}` };

			const r1 = await fetch('/api/v1/ledger/reconciliation', { headers });
			const r2 = await fetch('/api/v1/providers', { headers });
			const r3 = await fetch('/api/v1/rates/margin-report', { headers });
			return {
				reconciliation: r1.status,
				providers: r2.status,
				marginReport: r3.status
			};
		});
		expect(forbiddenChecks.reconciliation).toBe(403);
		expect(forbiddenChecks.providers).toBe(403);
		expect(forbiddenChecks.marginReport).toBe(403);

		// 4. Customer visits Wallets: must see ONLY its own organization wallet
		await page.goto('/wallets');
		await expect(page.locator('main h1')).toContainText('My Organization Wallet');

		// Only 1 wallet row should exist in the table for customer
		const walletRows = page.locator('tbody tr');
		await expect(walletRows).toHaveCount(1, { timeout: 10000 });

		// Click into own wallet
		await walletRows.first().locator('a[href*="/wallets/"]').click();
		await page.waitForURL(/\/wallets\/[a-f0-9-]+/, { timeout: 15000 });

		// Staff action buttons (+ Top-up, Adjust, Freeze) MUST NOT be visible for customer
		await expect(page.locator('button:has-text("+ Top-up")')).toHaveCount(0);
		await expect(page.locator('button:has-text("Adjustment")')).toHaveCount(0);
		await expect(page.locator('button:has-text("Freeze")')).toHaveCount(0);

		// Statements tab should be accessible and show statements
		await page.locator('button:has-text("Statements")').click();
		await expect(page.locator('button:has-text("Export CSV")')).toBeVisible();

		// 5. Customer visits Invoices: only customer's own invoices visible, create draft button hidden
		await page.goto('/invoices');
		await expect(page.locator('main h1')).toContainText('Invoices');
		await expect(page.locator('button:has-text("Create Draft Invoice")')).toHaveCount(0);
	});

	test('4. Reseller isolation: cross-tenant wallet and invoice access is refused (403)', async ({
		page
	}) => {
		await loginAs(page, resellerEmail);

		// 1. Reseller visits Wallets list
		await page.goto('/wallets');
		await expect(page.locator('main h1')).toContainText('Wallets');

		// 2. Reseller attempts direct API access to foreign customer wallet (Fintech Messaging Corp)
		const crossTenantCheck = await page.evaluate(async () => {
			const refreshRes = await fetch('/api/v1/auth/refresh', { method: 'POST', credentials: 'include' });
			const refreshJson = await refreshRes.json();
			const token = refreshJson?.data?.access_token || '';
			const headers = { Authorization: `Bearer ${token}` };

			const foreignWalletId = 'fa3e6ec8-a0c5-4776-844c-c25b76d3af76';
			const res = await fetch(`/api/v1/wallets/${foreignWalletId}`, { headers });
			const stmtRes = await fetch(`/api/v1/wallets/${foreignWalletId}/statement`, { headers });
			return {
				walletStatus: res.status,
				stmtStatus: stmtRes.status
			};
		});
		expect(crossTenantCheck.walletStatus).toBe(403);
		expect(crossTenantCheck.stmtStatus).toBe(403);

		// 3. Direct browser navigation to foreign wallet page displays error state
		await page.goto('/wallets/fa3e6ec8-a0c5-4776-844c-c25b76d3af76');
		await expect(page.locator('text=outside tenant boundary')).toBeVisible({ timeout: 10000 });
	});

	test('5. Platform admin creates draft invoice -> Issues gap-free invoice -> Records payment -> Verifies printable document', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		// 1. Navigate to Invoices
		await page.goto('/invoices');
		await expect(page.locator('main h1')).toContainText('Invoices & Billing');

		// 2. Open Create Draft Invoice modal
		await page.locator('button:has-text("Create Draft Invoice")').click();
		await expect(page.locator('h2:has-text("Draft Invoice")')).toBeVisible();

		// Fill customer and lines
		const customerSelect = page.locator('select#create-cust');
		await expect(customerSelect).toBeVisible();
		// Select first available customer
		await customerSelect.selectOption({ index: 0 });

		const suffix = Math.random().toString(36).substring(2, 7).toUpperCase();
		const lineDesc = `Playwright E2E Messaging Batch ${suffix}`;

		// Fill line item: description, quantity 1000, price 0.05
		const lineDescInput = page.locator('input[placeholder*="Line description"]').first();
		await lineDescInput.fill(lineDesc);

		const qtyInput = page.locator('input[placeholder="Qty"]').first();
		await qtyInput.fill('1000');

		const priceInput = page.locator('input[placeholder="Unit Price"]').first();
		await priceInput.fill('0.0500');

		// Tax
		await page.fill('input#create-taxlbl', 'VAT 10%');
		await page.fill('input#create-taxamt', '5.0000');

		// Save Draft
		await page.locator('button:has-text("Save Draft")').click();
		await expect(page.locator('h2:has-text("Draft Invoice")')).not.toBeVisible({
			timeout: 10000
		});

		// 3. Find created draft invoice in table
		const draftRow = page.locator('tr', { hasText: 'DRAFT' }).first();
		await expect(draftRow).toBeVisible({ timeout: 10000 });
		await expect(draftRow).toContainText('$ 55.0000');

		// 4. Issue the invoice
		page.on('dialog', (dialog) => dialog.accept());
		const issueBtn = draftRow.locator('button:has-text("Issue")');
		await expect(issueBtn).toBeVisible();
		await issueBtn.click();

		// Wait for this specific row to be issued and get its invoice number
		const targetRow = page.locator('tr', { hasText: '$ 55.0000' }).filter({ hasText: 'issued' }).first();
		await expect(targetRow).toBeVisible({ timeout: 10000 });
		const invLink = targetRow.locator('a[href*="/invoices/"]').first();
		await expect(invLink).toContainText(/INV-2026-\d{6}/);
		const invoiceNumber = (await invLink.textContent())?.trim() || '';

		// 5. Record full payment against this issued invoice
		const payBtn = targetRow.locator('button:has-text("Pay")');
		await expect(payBtn).toBeVisible();
		await payBtn.click();

		await expect(page.locator('h2:has-text("Record Manual Payment")')).toBeVisible();
		await page.fill('input#pmt-method', 'Bank Wire Transfer');
		await page.fill('input#pmt-ref', `WIRE-E2E-${suffix}`);
		await page.locator('button:has-text("Record Payment")').click();

		await expect(page.locator('h2:has-text("Record Manual Payment")')).not.toBeVisible({
			timeout: 10000
		});

		// Verify status changes to 'paid'
		await expect(page.locator(`tr:has-text("${invoiceNumber}")`)).toContainText('paid', {
			timeout: 10000
		});

		// 6. Click into invoice to view printable HTML invoice
		await page.locator(`tr:has-text("${invoiceNumber}")`).locator('a[href*="/invoices/"]').first().click();
		await page.waitForURL(/\/invoices\/[a-f0-9-]+/, { timeout: 15000 });

		// Verify printable document content inside iframe
		const frame = page.frameLocator('iframe[title="Invoice Document"]');
		await expect(frame.locator('body')).toContainText(invoiceNumber);
		await expect(frame.locator('body')).toContainText('PAID');
		await expect(frame.locator('body')).toContainText(lineDesc);
		await expect(frame.locator('body')).toContainText('VAT 10%');
		await expect(frame.locator('body')).toContainText('55.0000 USD');

		// Verify NO provider buy rate, provider connection, or internal secrets exposed
		const pageHtml = await page.content();
		expect(pageHtml).not.toContain('buy_rate');
		expect(pageHtml).not.toContain('upstream_cost');
		expect(pageHtml).not.toContain('znk_live_');
		expect(pageHtml).not.toContain('provider_secret');
	});
});