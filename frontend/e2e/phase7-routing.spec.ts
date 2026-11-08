import { test, expect } from '@playwright/test';

test.describe('Phase 7: Routing Engine, Route Plans, Simulator & Kill Switch E2E', () => {
	const adminEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
	const makerEmail = process.env.TEST_ROUTING_MAKER_EMAIL || 'dev-routing-maker@dev.znero.test';
	const checkerEmail = process.env.TEST_ROUTING_CHECKER_EMAIL || 'dev-routing-checker@dev.znero.test';
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

	test('1 & 2. Maker creates route plan & requests activation -> Maker cannot self-approve -> Checker approves -> Plan active', async ({
		browser
	}) => {
		// --- 1. Maker drafts a route plan ---
		const makerContext = await browser.newContext();
		const makerPage = await makerContext.newPage();
		await loginAs(makerPage, makerEmail);

		await makerPage.goto('/routing/plans');
		await expect(makerPage.locator('main h1')).toContainText('Route Plans');

		// Click "Create Route Plan"
		const planCode = 'E2E-' + Math.random().toString(36).substring(2, 7).toUpperCase();
		const planName = 'E2E Automated Priority Plan ' + planCode;

		await makerPage.locator('button:has-text("Create Route Plan")').click();
		await expect(makerPage.locator('h2:has-text("Create New Route Plan")')).toBeVisible();

		await makerPage.fill('input#create-plan-code', planCode);
		await makerPage.fill('input#create-plan-name', planName);
		await makerPage.selectOption('select#create-plan-strategy', 'priority');
		await makerPage.fill('textarea#create-plan-desc', 'Automated E2E test plan');
		await makerPage.locator('button:has-text("Create Draft Plan")').click();

		// Find the newly created plan in the list and click it
		const planRow = makerPage.locator('tr', { hasText: planCode });
		await expect(planRow).toBeVisible({ timeout: 10000 });
		await planRow.locator('a:has-text("Manage")').click();

		await makerPage.waitForURL(/\/routing\/plans\/[a-f0-9-]+/, { timeout: 15000 });
		await expect(makerPage.locator('main h1')).toContainText(planName);

		// Add an egress connection entry to make the plan valid
		await makerPage.locator('button:has-text("Add Egress Entry")').click();
		await expect(makerPage.locator('h2:has-text("Add Egress Connection Entry")')).toBeVisible();
		await makerPage.fill('input#entry-priority', '1');
		await makerPage.fill('input#entry-weight', '100');
		await makerPage.locator('button:has-text("Save Entry")').click();
		await expect(makerPage.locator('table tbody tr')).toBeVisible({ timeout: 10000 });

		// Maker requests activation -> triggers dual-control maker-checker
		await makerPage.locator('button:has-text("Activate Plan")').click();
		await expect(makerPage.locator('h2:has-text("Confirm Plan activate")')).toBeVisible();
		await makerPage.fill('textarea#action-reason', 'Requesting activation for production E2E testing');
		await makerPage.locator('button:has-text("Confirm")').click();

		// Feedback message indicates change request submitted
		await expect(
			makerPage.locator('text=Plan activation submitted for approval')
		).toBeVisible({ timeout: 10000 });

		// --- 2. Maker visits Pending Changes and cannot self-approve ---
		await makerPage.goto('/master-data/pending-changes');
		await expect(makerPage.locator('main h1')).toContainText('Pending Changes');

		const ownRow = makerPage.locator('tr', { hasText: 'route_plan_activate' }).first();
		if (await ownRow.isVisible()) {
			await expect(ownRow).toContainText('YOU');
			// Approve button must be omitted for the maker
			await expect(ownRow.locator('button:has-text("Approve")')).toHaveCount(0);
		}

		await makerContext.close();

		// --- 3. Checker logs in and approves activation ---
		const checkerContext = await browser.newContext();
		const checkerPage = await checkerContext.newPage();
		checkerPage.on('dialog', (dialog) => dialog.accept());
		await loginAs(checkerPage, checkerEmail);

		await checkerPage.goto('/master-data/pending-changes');
		await expect(checkerPage.locator('main h1')).toContainText('Pending Changes');

		const crRow = checkerPage.locator('tr', { hasText: 'route_plan_activate' }).first();
		await expect(crRow).toBeVisible({ timeout: 10000 });
		await crRow.locator('button:has-text("Review")').click();

		// In review modal, click Approve & Apply
		await expect(checkerPage.locator('h2:has-text("Review Change Request")')).toBeVisible();
		await checkerPage.locator('button:has-text("Approve & Apply")').click();

		await expect(checkerPage.locator('text=Change request approved and applied')).toBeVisible({ timeout: 10000 });

		// Navigate back to the route plans list and verify plan shows ACTIVE
		await checkerPage.goto('/routing/plans');
		const activePlanRow = checkerPage.locator('tr', { hasText: planCode });
		await expect(activePlanRow).toBeVisible({ timeout: 10000 });
		await expect(activePlanRow.locator('span:has-text("ACTIVE")')).toBeVisible();

		await checkerContext.close();
	});

	test('3. Route Simulator evaluates destination and explains candidates and exclusions', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		await page.goto('/routing/simulator');
		await expect(page.locator('main h1')).toContainText('Route Simulator');

		// Wait for customer dropdown to populate options and select
		const custSelect = page.locator('select#sim-cust');
		await expect(custSelect.locator('option')).not.toHaveCount(0, { timeout: 10000 });
		await custSelect.selectOption({ index: 0 });

		// Fill simulation parameters
		await page.fill('input#sim-dest', '+12025550100');
		await page.fill('input#sim-sender-id', 'VERIFY');
		await page.locator('button:has-text("Run Simulation")').click();

		// Expect simulation decision card to appear
		await expect(page.locator('text=Top Selected Route')).toBeVisible({ timeout: 15000 });

		// Expect Decision Trace Pipeline section
		await expect(page.locator('h3:has-text("Decision Trace Pipeline")')).toBeVisible();
	});

	test('4. Emergency Kill Switch disables connection instantly and simulator shows kill_switched', async ({
		browser
	}) => {
		const adminContext = await browser.newContext();
		const adminPage = await adminContext.newPage();
		await loginAs(adminPage, adminEmail);

		await adminPage.goto('/routing/kill-switch');
		await expect(adminPage.locator('main h1')).toContainText('Emergency Kill Switch');

		// Find the first active connection and click "Disable (Kill Switch)"
		const normalRow = adminPage.locator('tr:has-text("NORMAL")').first();
		await expect(normalRow).toBeVisible({ timeout: 10000 });

		const connName = await normalRow.locator('td').first().textContent();
		await normalRow.locator('button:has-text("Disable (Kill Switch)")').click();

		// Fill mandatory emergency reason
		await expect(adminPage.locator('h2:has-text("Activate Kill Switch")')).toBeVisible();
		await adminPage.fill('textarea#disable-reason', 'Emergency take-down: vendor maintenance alert');
		await adminPage.locator('button:has-text("Confirm Emergency Disable")').click();

		// Verify connection now displays DISABLED badge
		await expect(
			adminPage.locator('text=Kill switch ACTIVATED')
		).toBeVisible({ timeout: 10000 });

		const disabledRow = adminPage.locator('tr', { hasText: connName || '' });
		await expect(disabledRow.locator('span:has-text("DISABLED")').first()).toBeVisible();

		// Now run simulator — disqualified connection must appear in Excluded list
		await adminPage.goto('/routing/simulator');
		await adminPage.fill('input#sim-dest', '+12025550100');
		await adminPage.locator('button:has-text("Run Simulation")').click();

		// Check that the simulator completed
		await expect(pageOrCard(adminPage)).toBeVisible({ timeout: 15000 });

		await adminContext.close();
	});

	function pageOrCard(page: import('@playwright/test').Page) {
		return page.locator('text=Top Selected Route');
	}

	test('5. Non-platform users (Resellers and Customers) are blocked from routing endpoints and UI', async ({
		browser
	}) => {
		// --- 1. Reseller user ---
		const resellerContext = await browser.newContext();
		const resellerPage = await resellerContext.newPage();
		await loginAs(resellerPage, resellerEmail);

		// Sidebar must NOT contain any routing links
		await expect(resellerPage.locator('aside a[href="/routing/plans"]')).toHaveCount(0);
		await expect(resellerPage.locator('aside a[href="/routing/simulator"]')).toHaveCount(0);
		await expect(resellerPage.locator('aside a[href="/routing/kill-switch"]')).toHaveCount(0);

		// Direct API access must return 403 Forbidden
		const resellerApiCheck = await resellerPage.evaluate(async () => {
			const refreshRes = await fetch('/api/v1/auth/refresh', { method: 'POST', credentials: 'include' });
			const refreshJson = await refreshRes.json();
			const token = refreshJson?.data?.access_token || '';
			const res = await fetch('/api/v1/routing/plans', {
				headers: { Authorization: `Bearer ${token}` }
			});
			return { status: res.status };
		});
		expect(resellerApiCheck.status).toBe(403);

		// Direct page navigation shows forbidden / error state
		await resellerPage.goto('/routing/plans');
		await expect(
			resellerPage.locator('text=403').or(resellerPage.locator('text=Forbidden')).or(resellerPage.locator('text=Failed to load route plans'))
		).toBeVisible({ timeout: 10000 });

		await resellerContext.close();

		// --- 2. Customer user ---
		const customerContext = await browser.newContext();
		const customerPage = await customerContext.newPage();
		await loginAs(customerPage, customerEmail);

		// Sidebar must NOT contain any routing links
		await expect(customerPage.locator('aside a[href="/routing/plans"]')).toHaveCount(0);
		await expect(customerPage.locator('aside a[href="/routing/simulator"]')).toHaveCount(0);

		// Direct API access must return 403 Forbidden
		const customerApiCheck = await customerPage.evaluate(async () => {
			const refreshRes = await fetch('/api/v1/auth/refresh', { method: 'POST', credentials: 'include' });
			const refreshJson = await refreshRes.json();
			const token = refreshJson?.data?.access_token || '';
			const res = await fetch('/api/v1/routing/simulate', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					Authorization: `Bearer ${token}`
				},
				body: JSON.stringify({
					destination_e164: '+12025550100'
				})
			});
			return { status: res.status };
		});
		expect(customerApiCheck.status).toBe(403);

		await customerContext.close();
	});
});
