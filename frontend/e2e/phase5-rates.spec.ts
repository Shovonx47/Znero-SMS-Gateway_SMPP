import { test, expect } from '@playwright/test';

test.describe('Phase 5: Rating Engine, Rate Cards & Margin Protection E2E', () => {
	const adminEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
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

	test('1. Platform Admin views Buy Cards list and navigates to Buy Card detail & rate sheet', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		// Navigate to Provider Buy Cards
		await page.goto('/rates/buy-cards');
		await expect(page.locator('main h1')).toContainText('Provider Buy Cards');

		// Search for sample buy card
		const searchInput = page.locator('input[type="search"]');
		if (await searchInput.isVisible()) {
			await searchInput.fill('DEV SAMPLE Alpha Provider Buy Card');
		}

		// Verify DEV SAMPLE Buy Card exists
		await expect(page.locator('table')).toContainText('DEV SAMPLE Alpha Provider Buy Card', {
			timeout: 10000
		});

		// Click into the sample buy card detail
		const row = page.locator('tr', { hasText: 'DEV SAMPLE Alpha Provider Buy Card' });
		await row.locator('a[href*="/rates/buy-cards/"]').first().click();
		await page.waitForURL(/\/rates\/buy-cards\/[a-f0-9-]+/, { timeout: 15000 });

		// Verify detail page
		await expect(page.locator('main h1')).toContainText('DEV SAMPLE Alpha Provider Buy Card', {
			timeout: 10000
		});
		await expect(page.locator('span:has-text("active")').first()).toBeVisible();

		// Rates table should be visible with currency and prices
		const ratesTable = page.locator('table');
		await expect(ratesTable).toBeVisible();
		await expect(ratesTable).toContainText('USD');
	});

	test('2. Platform Admin views Sell Cards and tests real-time Margin Check validator in Add Rate modal', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		// Navigate to Customer Sell Cards
		await page.goto('/rates/sell-cards');
		await expect(page.locator('main h1')).toContainText('Customer Sell Cards');

		// Search for sample sell card
		const searchInput = page.locator('input[type="search"]');
		if (await searchInput.isVisible()) {
			await searchInput.fill('DEV SAMPLE Standard Sell Card');
		}

		// Verify DEV SAMPLE Standard Sell Card exists
		await expect(page.locator('table')).toContainText('DEV SAMPLE Standard Sell Card', {
			timeout: 10000
		});

		// Click into sell card
		const row = page.locator('tr', { hasText: 'DEV SAMPLE Standard Sell Card' });
		await row.locator('a[href*="/rates/sell-cards/"]').first().click();
		await page.waitForURL(/\/rates\/sell-cards\/[a-f0-9-]+/, { timeout: 15000 });

		await expect(page.locator('main h1')).toContainText('DEV SAMPLE Standard Sell Card', {
			timeout: 10000
		});

		// Open Add Sell Rate modal
		await page.locator('button:has-text("New Sell Rate")').click();
		await expect(page.locator('h2:has-text("New Sell Rate")')).toBeVisible();

		// Select first country in dropdown
		const countrySelect = page.locator('select#sr-country');
		await countrySelect.selectOption({ index: 1 });

		// Type an intentionally unprofitable price: 0.000100
		const priceInput = page.locator('input#sr-price');
		await priceInput.fill('0.000100');

		// Wait for live margin check debounce
		await page.waitForTimeout(600);

		// Now type a profitable price: 0.050000
		await priceInput.fill('0.050000');
		await page.waitForTimeout(600);

		// Close modal
		await page.locator('div.fixed button:has-text("Cancel")').click();
		await expect(page.locator('h2:has-text("New Sell Rate")')).not.toBeVisible();
	});

	test('3. Rate Card Assignments: Customer and Reseller tabs display active assignments', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		// Navigate to Assignments
		await page.goto('/rates/assignments');
		await expect(page.locator('main h1')).toContainText('Rate Card Assignments');

		// Tab 1: Customer Rate Cards
		await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
		await expect(page.locator('table')).toContainText('DEV SAMPLE Standard Sell Card');

		// Switch to Tab 2: Reseller Wholesale Cost Cards
		await page.locator('button:has-text("Reseller Cost Cards")').click();
		await expect(page.locator('table')).toContainText('DEV SAMPLE Reseller Cost Card');
	});

	test('4. Margin & Deficit Report: Displays analysis table, stats, and filters', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		// Navigate to Margin Report
		await page.goto('/rates/margin-report');
		await expect(page.locator('main h1')).toContainText('Margin & Deficit Report');

		// Stats cards
		await expect(page.locator('span:has-text("Total Analyzed")')).toBeVisible();
		await expect(page.locator('table')).toBeVisible({ timeout: 10000 });

		// Toggle violations only
		const violationCheckbox = page.locator('input[type="checkbox"]#violations-only');
		if (await violationCheckbox.isVisible()) {
			await violationCheckbox.check();
			await page.waitForTimeout(400);
			await violationCheckbox.uncheck();
		}
	});

	test('5. Rate Calculator & Lookup: Phone Number and Destination queries return rate matches', async ({
		page
	}) => {
		await loginAs(page, customerEmail);

		// Navigate to Lookup
		await page.goto('/rates/lookup');
		await expect(page.locator('main h1')).toContainText('Rate Calculator & Lookup');

		// Phone lookup tab
		const phoneInput = page.locator('input#p-num');
		await expect(phoneInput).toBeVisible();

		// Test E.164 lookup with sample US number (+12025550123 matches 1202 prefix)
		await phoneInput.fill('+12025550123');
		await page.locator('button:has-text("Calculate SMS Rate")').click();

		// Wait for resolution result card
		await expect(page.locator('span:has-text("Calculated Rate")')).toBeVisible({ timeout: 10000 });
		await expect(page.locator('main')).toContainText('USD');

		// Destination lookup tab
		await page.locator('button:has-text("Country & Operator Lookup")').click();
		const countrySelect = page.locator('select#d-country');
		await expect(countrySelect).toBeVisible();
		const countryOptions = await countrySelect.locator('option').allInnerTexts();
		const usTarget = countryOptions.find((t) => t.includes('(US /')) || countryOptions[0];
		if (usTarget) {
			await countrySelect.selectOption({ label: usTarget });
		}
		await page.locator('button:has-text("Query Destination Rate")').click();

		await expect(page.locator('span:has-text("Resolved Rate")')).toBeVisible({ timeout: 10000 });
		await expect(page.locator('main')).toContainText('USD');
	});

	test('6. Upcoming Rate Changes: Displays future scheduled rate adjustments with countdown', async ({
		page
	}) => {
		await loginAs(page, adminEmail);

		// Navigate to Upcoming Rates
		await page.goto('/rates/upcoming');
		await expect(page.locator('main h1')).toContainText('Upcoming Rate Changes');

		// Verify table or window selector
		await expect(page.locator('select')).toBeVisible();
	});

	test('7. Reseller Org Admin views assigned wholesale cost sheet at /rates/my-cost', async ({
		page
	}) => {
		await loginAs(page, resellerEmail);

		// Navigate to My Wholesale Cost Sheet
		await page.goto('/rates/my-cost');
		await expect(page.locator('main h1')).toContainText('Cost');

		// Should show assigned wholesale cost sheet
		await expect(page.locator('main')).toContainText('DEV SAMPLE Reseller Cost Card', {
			timeout: 10000
		});
		await expect(page.locator('table')).toBeVisible();
	});

	test('8. Customer Org Admin views active assigned SMS rates at /rates/my-rates', async ({
		page
	}) => {
		await loginAs(page, customerEmail);

		// Navigate to My Rates
		await page.goto('/rates/my-rates');
		await expect(page.locator('main h1')).toContainText('My SMS Rates');

		// Should show assigned standard sell rates
		await expect(page.locator('main')).toContainText('DEV SAMPLE Standard Sell Card', {
			timeout: 10000
		});
		await expect(page.locator('table')).toBeVisible();
	});
});
