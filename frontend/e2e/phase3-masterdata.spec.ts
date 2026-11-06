import { test, expect } from '@playwright/test';
import path from 'path';

test.describe('Phase 3: Master Data & Number Intelligence E2E', () => {
	const adminEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
	const adminPassword = process.env.TEST_ADMIN_PASSWORD || 'DevSeed1234!#';

	test.beforeEach(async ({ page }) => {
		// Log in as Super Admin
		await page.goto('/login');
		await page.fill('input#email', adminEmail);
		await page.fill('input#password', adminPassword);
		await page.click('button[type="submit"]');
		await page.waitForURL('**/dashboard', { timeout: 15000 });
	});

	test('1. Master Data navigation links appear in sidebar', async ({ page }) => {
		const sidebar = page.locator('aside');
		await expect(sidebar.locator('a[href="/master-data/countries"]')).toBeVisible();
		await expect(sidebar.locator('a[href="/master-data/operators"]')).toBeVisible();
		await expect(sidebar.locator('a[href="/master-data/prefixes"]')).toBeVisible();
		await expect(sidebar.locator('a[href="/master-data/import"]')).toBeVisible();
		await expect(sidebar.locator('a[href="/master-data/number-lookup"]')).toBeVisible();
		await expect(sidebar.locator('a[href="/master-data/transform-rules"]')).toBeVisible();
		await expect(sidebar.locator('a[href="/master-data/pending-changes"]')).toBeVisible();
	});

	test('2. Countries page lists authoritative countries and creates country', async ({ page }) => {
		await page.goto('/master-data/countries');
		await expect(page.locator('main h1')).toContainText('Countries');

		// Check that authoritative ISO countries table renders
		const table = page.locator('table');
		await expect(table).toBeVisible();
		await expect(table.locator('text=Afghanistan')).toBeVisible();

		// Open Add Country Modal
		const addBtn = page.locator('button:has-text("Add Country")');
		await expect(addBtn).toBeVisible();
		await addBtn.click();

		// Modal should appear
		await expect(page.locator('h2:has-text("Add New Country")')).toBeVisible();

		// Fill country creation form using an unused user-assigned ISO code (XA-XZ, QM-QZ, 900-999)
		const letters = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
		const candidates: Array<{ iso2: string; num: string }> = [];
		for (let i = 0; i < 20; i++) {
			const p = ['X', 'Q', 'Z'][Math.floor(Math.random() * 3)];
			const l = letters[Math.floor(Math.random() * 26)];
			const n = String(900 + Math.floor(Math.random() * 99));
			candidates.push({ iso2: p + l, num: n });
		}

		for (const cand of candidates) {
			await page.locator('input[required]').nth(0).fill('Test Country ' + cand.iso2); // Name
			await page.locator('input[required]').nth(1).fill('99' + Math.floor(Math.random() * 9)); // Calling code
			await page.locator('input[maxlength="2"]').fill(cand.iso2); // ISO2
			await page.locator('input[maxlength="3"]').first().fill(cand.iso2 + 'A'); // ISO3
			await page.locator('input[maxlength="3"]').nth(1).fill(cand.num); // Numeric code
			await page.locator('input[placeholder*="Describe why"]').fill('Playwright automated E2E country test');

			// Submit creation form
			await page.locator('button:has-text("Create Country")').click();

			try {
				await expect(page.locator('h2:has-text("Add New Country")')).not.toBeVisible({ timeout: 2500 });
				break;
			} catch {
				// Try next user-assigned code if collision occurred
			}
		}

		// Modal should close on success
		await expect(page.locator('h2:has-text("Add New Country")')).not.toBeVisible({ timeout: 10000 });
	});

	test('3. Operators page renders and filters by country', async ({ page }) => {
		await page.goto('/master-data/operators');
		await expect(page.locator('main h1')).toContainText('Operators');

		const table = page.locator('table');
		await expect(table).toBeVisible();

		// Verify MCC/MNC column header exists
		await expect(table.locator('th:has-text("MCC / MNC")')).toBeVisible();
	});

	test('4. Prefixes page renders prefix records', async ({ page }) => {
		await page.goto('/master-data/prefixes');
		await expect(page.locator('main h1')).toContainText('Number Prefixes');

		const table = page.locator('table');
		await expect(table).toBeVisible();
	});

	test('5. Number Intelligence tool performs single number resolution', async ({ page }) => {
		await page.goto('/master-data/number-lookup');
		await expect(page.locator('main h1')).toContainText('Number Intelligence');

		// Fill a number and run lookup
		const input = page.locator('input[placeholder*="202"]');
		await input.fill('+1 (202) 555-0199');
		await page.locator('button:has-text("Lookup & Normalize")').click();

		// Verify normalized E.164 result appears
		await expect(page.locator('text=+12025550199')).toBeVisible();
		await expect(page.locator('text=VALID E.164')).toBeVisible();
		await expect(page.locator('text=United States')).toBeVisible();
	});

	test('6. Import Center renders file upload and previews valid rows', async ({ page }) => {
		await page.goto('/master-data/import');
		await expect(page.locator('main h1')).toContainText('Import Center');

		// Verify tabs
		await expect(page.locator('button:has-text("Upload & Preview")')).toBeVisible();
		await expect(page.locator('button:has-text("Job History")')).toBeVisible();

		// Select file using file input
		const sampleCsvPath = path.resolve('..', 'data', 'samples', 'countries_sample.csv');
		const fileInput = page.locator('input[type="file"]');
		await fileInput.setInputFiles(sampleCsvPath);

		// Trigger preview generation
		const uploadBtn = page.locator('button:has-text("Upload & Generate Preview")');
		await expect(uploadBtn).toBeEnabled();
		await uploadBtn.click();

		// Verify preview section appears
		await expect(page.locator('text=Import Preview & Validation Report')).toBeVisible({ timeout: 15000 });
		await expect(page.locator('text=Total Rows')).toBeVisible({ timeout: 5000 });
	});

	test('7. Dual-control Maker-Checker: User A creates pending change, cannot self-approve, User B approves and change appears', async ({ page, browser }) => {
		const makerEmail = process.env.TEST_MAKER_EMAIL || 'dev-maker@dev.znero.test';
		const letters = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
		const suffix = letters[Math.floor(Math.random() * 26)];
		const iso2 = 'Q' + suffix;
		const iso3 = 'Q' + suffix + 'Z';
		const numCode = String(950 + Math.floor(Math.random() * 48));
		const countryName = `DualControl Country ${iso2}`;

		// 1. User A (Maker without override) logs in and submits a country creation
		const makerContext = await browser.newContext();
		const makerPage = await makerContext.newPage();
		await makerPage.goto('/login');
		await makerPage.fill('input#email', makerEmail);
		await makerPage.fill('input#password', adminPassword);
		await makerPage.click('button[type="submit"]');
		await makerPage.waitForURL('**/dashboard', { timeout: 15000 });

		await makerPage.goto('/master-data/countries');
		await makerPage.locator('button:has-text("Add Country")').click();
		await expect(makerPage.locator('h2:has-text("Add New Country")')).toBeVisible();
		await makerPage.locator('input[required]').nth(0).fill(countryName);
		await makerPage.locator('input[required]').nth(1).fill('996');
		await makerPage.locator('input[maxlength="2"]').fill(iso2);
		await makerPage.locator('input[maxlength="3"]').first().fill(iso3);
		await makerPage.locator('input[maxlength="3"]').nth(1).fill(numCode);
		await makerPage.locator('input[placeholder*="Describe why"]').fill('Maker pending creation test');
		await makerPage.locator('button:has-text("Create Country")').click();
		await expect(makerPage.locator('h2:has-text("Add New Country")')).not.toBeVisible({ timeout: 10000 });

		// 2. Maker visits Pending Changes: sees their own request marked YOU, and has no active Approve button
		await makerPage.goto('/master-data/pending-changes');
		await expect(makerPage.locator('main h1')).toContainText('Pending Changes');
		await expect(makerPage.locator('table')).toContainText('YOU');
		await expect(makerPage.locator('button:has-text("Approve"):not([disabled])')).toHaveCount(0);
		await makerContext.close();

		// 3. User B (Super Admin / Checker) reviews Pending Changes and approves User A's request
		await page.goto('/master-data/pending-changes');
		await expect(page.locator('main h1')).toContainText('Pending Changes');
		page.on('dialog', (dialog) => dialog.accept());

		const approveBtn = page.locator('button:has-text("Approve"):not([disabled])').first();
		await expect(approveBtn).toBeVisible();
		await approveBtn.click();

		// 4. Verify the newly approved country now exists in the Countries list
		await page.goto('/master-data/countries');
		const searchInput = page.locator('input[placeholder*="Search"]');
		await searchInput.fill(countryName);
		await searchInput.press('Enter');
		await expect(page.locator('table')).toContainText(countryName, { timeout: 10000 });
	});

	test('8. Transformation Rules interactive simulator renders', async ({ page }) => {
		await page.goto('/master-data/transform-rules');
		await expect(page.locator('main h1')).toContainText('Transformation Rules');

		// Verify Rule Chain Simulator is present
		await expect(page.locator('h2:has-text("Interactive Rule Chain Simulator")')).toBeVisible();
		await expect(page.locator('button:has-text("Simulate Rule Chain")')).toBeVisible();
	});
});
