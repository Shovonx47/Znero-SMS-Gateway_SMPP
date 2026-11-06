import { test, expect } from '@playwright/test';

test.describe('Phase 4: Providers, Coverage, Customer Profiles, Credentials & Sender IDs E2E', () => {
	const adminEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
	const makerEmail = process.env.TEST_MAKER_EMAIL || 'dev-maker@dev.znero.test';
	const checkerEmail = process.env.TEST_CHECKER_EMAIL || 'dev-checker@dev.znero.test';
	const resellerEmail = process.env.TEST_RESELLER_EMAIL || 'dev-reseller-admin@dev.znero.test';
	const password = process.env.TEST_ADMIN_PASSWORD || 'DevSeed1234!#';

	async function loginAs(page: import('@playwright/test').Page, email: string) {
		await page.goto('/login');
		await page.fill('input#email', email);
		await page.fill('input#password', password);
		await page.click('button[type="submit"]');
		await page.waitForURL('**/dashboard', { timeout: 15000 });
	}

	test('1. Platform admin creates a provider and connection; secret is write-only and masked afterwards', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		// Navigate to Providers
		await page.goto('/providers');
		await expect(page.locator('main h1')).toContainText('Upstream Providers');

		const suffix = Math.random().toString(36).substring(2, 6).toUpperCase();
		const provName = `DEV SAMPLE Carrier ${suffix}`;
		const provCode = `DEV_E2E_${suffix}`;

		await page.locator('button:has-text("New Provider")').click();
		await expect(page.locator('h2:has-text("Create Upstream Provider")')).toBeVisible();

		await page.fill('input#prov-name', provName);
		await page.fill('input#prov-code', provCode);
		await page.selectOption('select#prov-type', 'smpp');
		await page.fill('input#prov-legal', 'Fictional E2E Carrier Corp');
		await page.fill('input#prov-reason', 'Playwright E2E provider creation test');
		await page.locator('button:has-text("Create Provider")').click();

		await expect(page.locator('h2:has-text("Create Upstream Provider")')).not.toBeVisible({
			timeout: 10000
		});
		await expect(page.locator('table')).toContainText(provCode, { timeout: 10000 });

		// Click Manage to go to Provider Detail
		const row = page.locator('tr', { hasText: provCode });
		await row.locator('a:has-text("Manage")').click();
		await expect(page.locator('main h1')).toContainText(provName);

		// Create an SMPP connection with a secret password
		const secretPlaintext = `E2eWriteOnlySecret_${suffix}_99!`;
		const connName = `DEV SAMPLE Bind ${suffix}`;

		await page.locator('button:has-text("+ New Connection")').click();
		await expect(page.locator('h2:has-text("Create Provider Connection")')).toBeVisible();

		await page.fill('input#conn-name', connName);
		await page.selectOption('select#conn-proto', 'smpp');
		await page.fill('input#conn-host', 'smpp.e2e-carrier.example.com');
		await page.fill('input#conn-port', '2775');
		await page.fill('input#conn-sysid', `e2e_${suffix.toLowerCase()}`);
		await page.fill('input#conn-pass', secretPlaintext);
		await page.fill('input#conn-reason', 'Playwright E2E connection creation with write-only secret');
		await page.locator('button:has-text("Create Connection")').click();

		await expect(page.locator('h2:has-text("Create Provider Connection")')).not.toBeVisible({
			timeout: 10000
		});

		// Verify connection card appears and shows masked badge '••• set', never the plaintext secret
		const connCard = page.locator('[data-testid="connection-card"]', { hasText: connName });
		await expect(connCard).toBeVisible();
		await expect(connCard.locator('[data-testid="secret-masked-badge"]')).toContainText('••• set');
		const pageContent = await page.content();
		expect(pageContent).not.toContain(secretPlaintext);
	});

	test('2. Maker changes connection status -> pending change created; Maker cannot approve own change; Checker approves -> connection becomes active', async ({
		browser
	}) => {
		// 1. Maker logs in and navigates to DEV SAMPLE Provider Alpha
		const makerCtx = await browser.newContext();
		const makerPage = await makerCtx.newPage();
		await loginAs(makerPage, makerEmail);

		await makerPage.goto('/providers');
		await expect(makerPage.locator('table')).toContainText('DEV_ALPHA');

		// Create a fresh draft connection as Maker so we have a dedicated connection to activate via Maker-Checker
		const suffix = Math.random().toString(36).substring(2, 6).toUpperCase();
		const connName = `DEV SAMPLE DualControl Bind ${suffix}`;

		const alphaRow = makerPage.locator('tr', { hasText: 'DEV_ALPHA' });
		await alphaRow.locator('a:has-text("Manage")').click();
		await expect(makerPage.locator('main h1')).toContainText('DEV SAMPLE Provider Alpha');

		await makerPage.locator('button:has-text("+ New Connection")').click();
		await makerPage.fill('input#conn-name', connName);
		await makerPage.fill('input#conn-host', 'smpp-dual.example.com');
		await makerPage.fill('input#conn-port', '2775');
		await makerPage.fill('input#conn-sysid', `dc_${suffix.toLowerCase()}`);
		await makerPage.fill('input#conn-pass', `RuntimePass_${suffix}!`);
		await makerPage.fill('input#conn-reason', 'Create draft bind for dual-control activation test');
		await makerPage.locator('button:has-text("Create Connection")').click();
		await expect(makerPage.locator('h2:has-text("Create Provider Connection")')).not.toBeVisible({
			timeout: 10000
		});

		// Maker clicks Change Status on that draft connection -> sets to 'active'
		const connCard = makerPage.locator('[data-testid="connection-card"]', { hasText: connName });
		await expect(connCard).toContainText('draft');
		await connCard.locator('button:has-text("Change Status")').click();

		await expect(makerPage.locator('h2:has-text("Change Connection Status")')).toBeVisible();
		await makerPage.selectOption('select#next-conn-status', 'active');
		await makerPage.fill('input#conn-status-reason', `Maker requesting activation of ${connName}`);
		await makerPage.locator('button:has-text("Submit Status Change")').click();
		await expect(makerPage.locator('h2:has-text("Change Connection Status")')).not.toBeVisible({
			timeout: 10000
		});

		// Connection remains 'draft' while pending approval
		await expect(connCard).toContainText('draft');

		// 2. Maker visits Pending Changes -> sees their request marked YOU and cannot approve it
		await makerPage.goto('/master-data/pending-changes');
		await expect(makerPage.locator('main h1')).toContainText('Pending Changes');
		await expect(makerPage.locator('table')).toContainText('provider_connection');
		await expect(makerPage.locator('table')).toContainText('YOU');
		await expect(makerPage.locator('button:has-text("Approve"):not([disabled])')).toHaveCount(0);
		await makerCtx.close();

		// 3. Checker logs in, visits Pending Changes, and approves Maker's connection status change
		const checkerCtx = await browser.newContext();
		const checkerPage = await checkerCtx.newPage();
		await loginAs(checkerPage, checkerEmail);

		await checkerPage.goto('/master-data/pending-changes');
		await expect(checkerPage.locator('main h1')).toContainText('Pending Changes');
		checkerPage.on('dialog', (dialog) => dialog.accept());

		const approveBtn = checkerPage.locator('button:has-text("Approve"):not([disabled])').first();
		await expect(approveBtn).toBeVisible();
		await approveBtn.click();

		// 4. Verify connection is now active on the Provider Detail page
		await checkerPage.goto('/providers');
		const checkerAlphaRow = checkerPage.locator('tr', { hasText: 'DEV_ALPHA' });
		await checkerAlphaRow.locator('a:has-text("Manage")').click();
		const updatedConnCard = checkerPage.locator('[data-testid="connection-card"]', {
			hasText: connName
		});
		await expect(updatedConnCard).toContainText('active', { timeout: 10000 });
		await checkerCtx.close();
	});

	test('3. Reseller logs in -> cannot see Providers in sidebar and gets 403 when visiting /providers', async ({
		page
	}) => {
		await loginAs(page, resellerEmail);

		// Verify sidebar does NOT show Providers or Provider Coverage links
		const sidebar = page.locator('aside');
		await expect(sidebar).toBeVisible();
		await expect(sidebar.locator('a[href="/providers"]')).toHaveCount(0);
		await expect(sidebar.locator('a[href="/provider-coverage"]')).toHaveCount(0);

		// Visiting /providers directly renders 403 Forbidden message
		await page.goto('/providers');
		await expect(page.locator('main')).toContainText('403 Forbidden', { timeout: 10000 });

		// Also verify direct backend API call with Reseller's Bearer token returns HTTP 403
		const apiStatus = await page.evaluate(async () => {
			const refreshRes = await fetch('/api/v1/auth/refresh', {
				method: 'POST',
				credentials: 'include'
			});
			const refreshJson = await refreshRes.json();
			const accessToken = refreshJson?.data?.access_token || '';
			const res = await fetch('/api/v1/providers', {
				headers: { Authorization: `Bearer ${accessToken}` }
			});
			return res.status;
		});
		expect(apiStatus).toBe(403);
	});

	test('4. Creating a customer API key shows the one-time key with warning; closing and reloading shows only the prefix', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		await page.goto('/customers');
		await expect(page.locator('main h1')).toContainText('Customer Commercial Profiles & Credentials');

		// Switch to API Keys tab
		await page.locator('button:has-text("API Keys (")').click();

		const suffix = Math.random().toString(36).substring(2, 6).toUpperCase();
		const keyLabel = `DEV SAMPLE E2E Key ${suffix}`;

		await page.locator('button:has-text("+ Create API Key")').click();
		await expect(page.locator('h2:has-text("Create Customer API Key")')).toBeVisible();

		await page.fill('input#ak-name', keyLabel);
		await page.fill('input#ak-scopes', 'sms:send,dlr:read');
		await page.fill('input#ak-reason', 'Playwright E2E one-time API key test');
		await page.locator('button:has-text("Generate API Key")').click();

		// One-Time Secret Modal must appear with warning and full key
		const modal = page.locator('[data-testid="one-time-secret-modal"]');
		await expect(modal).toBeVisible({ timeout: 10000 });
		await expect(modal).toContainText('never be shown again');

		const secretValueEl = modal.locator('[data-testid="one-time-secret-value"]');
		await expect(secretValueEl).toBeVisible();
		const fullPlaintextKey = (await secretValueEl.textContent())?.trim() || '';
		expect(fullPlaintextKey.startsWith('znk_live_')).toBe(true);
		expect(fullPlaintextKey.length).toBeGreaterThan(24);
		const expectedPrefix = fullPlaintextKey.slice(0, 17);

		// Close modal
		await modal.locator('[data-testid="close-one-time-secret-btn"]').click();
		await expect(modal).not.toBeVisible();

		// Reload page and return to API Keys tab -> verify only prefix is shown, NEVER full key
		await page.reload();
		await page.locator('button:has-text("API Keys (")').click();

		const keyRow = page.locator('tr', { hasText: keyLabel });
		await expect(keyRow).toBeVisible();
		await expect(keyRow.locator('[data-testid="apikey-prefix-badge"]')).toContainText(
			`${expectedPrefix}...`
		);

		const htmlAfterReload = await page.content();
		expect(htmlAfterReload).not.toContain(fullPlaintextKey);
	});

	test('5. Submitting a Sender ID request creates a pending row; approving it changes status to approved', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		await page.goto('/sender-ids');
		await expect(page.locator('main h1')).toContainText('Sender ID Registry');

		const digits = String(Math.floor(1000 + Math.random() * 8999));
		const senderVal = `ZNR-${digits}`;

		await page.locator('button:has-text("Submit Sender ID")').click();
		await expect(page.locator('h2:has-text("Submit Sender ID Request")')).toBeVisible();

		// Select first customer organization
		const orgSelect = page.locator('select#sid-org');
		const firstOrgValue = await orgSelect.locator('option').nth(1).getAttribute('value');
		if (firstOrgValue) {
			await orgSelect.selectOption(firstOrgValue);
		}

		await page.fill('input#sid-val', senderVal);
		await page.selectOption('select#sid-type', 'alphanumeric');
		await page.fill('input#sid-reason', 'Playwright E2E Sender ID registration');
		await page.locator('div.fixed button:has-text("Submit Sender ID")').click();

		await expect(page.locator('h2:has-text("Submit Sender ID Request")')).not.toBeVisible({
			timeout: 10000
		});

		// Verify row starts in 'pending' status
		const sidRow = page.locator('tr', { hasText: senderVal });
		await expect(sidRow).toBeVisible({ timeout: 10000 });
		await expect(sidRow).toContainText('pending');

		// Approve the Sender ID
		await sidRow.locator('button:has-text("Approve")').click();

		// Verify status changes to 'approved'
		await expect(sidRow).toContainText('approved', { timeout: 10000 });
	});
});
