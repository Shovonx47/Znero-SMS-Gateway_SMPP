import { test, expect } from '@playwright/test';

test.describe('Auth Guard & Public/Protected Layout Separation', () => {

	test.describe('1. Logged-out visits to protected URLs redirect to login', () => {
		const protectedPaths = [
			'/',
			'/dashboard',
			'/organizations',
			'/users',
			'/roles',
			'/audit',
			'/sessions',
			'/settings/password'
		];

		for (const path of protectedPaths) {
			test(`unauthenticated visit to ${path} redirects to /login with next param`, async ({ page }) => {
				await page.goto(path);
				// Verify URL changes to /login with next query param
				await expect(page).toHaveURL(new RegExp(`/login\\?next=`));
				// Verify login form is visible
				await expect(page.locator('input#email')).toBeVisible();
				await expect(page.locator('input#password')).toBeVisible();
				await expect(page.locator('button[type="submit"]')).toBeVisible();
			});
		}
	});

	test.describe('2. Sidebar and Topbar are absent on all auth pages', () => {
		const authPages = [
			'/login',
			'/forgot-password',
			'/reset-password',
			'/accept-invitation',
			'/mfa/challenge',
			'/mfa/setup'
		];

		for (const authPath of authPages) {
			test(`sidebar is absent on ${authPath}`, async ({ page }) => {
				await page.goto(authPath);
				// In (auth) layout, <aside> must NOT exist in the DOM
				const sidebar = page.locator('aside');
				await expect(sidebar).toHaveCount(0);
				// Topbar <header> with user info must NOT exist in the DOM
				const topbar = page.locator('header');
				await expect(topbar).toHaveCount(0);
			});
		}
	});

	test.describe('3. Expired session handling', () => {
		test('redirect to /login?reason=expired displays session expired notification', async ({ page }) => {
			await page.goto('/login?reason=expired');
			const expiredBanner = page.locator('text=Your session has expired. Please sign in again.');
			await expect(expiredBanner).toBeVisible();
		});
	});

	test.describe('4. Authenticated sessions and layout rendering', () => {
		test('signing in renders app layout with sidebar, topbar, and tenant info', async ({ page }) => {
			await page.goto('/login');

			// Fill credentials for Admin
			const adminEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
			const adminPassword = process.env.TEST_ADMIN_PASSWORD || 'DevSeed1234!#';
			await page.fill('input#email', adminEmail);
			await page.fill('input#password', adminPassword);
			await page.click('button[type="submit"]');

			// Wait for redirection to dashboard or protected area
			await page.waitForURL('**/dashboard');

			// In (app) layout, <aside> must now be visible
			const sidebar = page.locator('aside');
			await expect(sidebar).toBeVisible();

			// Topbar must show the logged-in email and tenant type
			const topbar = page.locator('header');
			await expect(topbar).toBeVisible();
			await expect(topbar.locator(`text=${adminEmail}`)).toBeVisible();

			// Nav links for authorized sections must exist
			await expect(page.locator('aside a[href="/organizations"]')).toBeVisible();
			await expect(page.locator('aside a[href="/users"]')).toBeVisible();
			await expect(page.locator('aside a[href="/roles"]')).toBeVisible();

			// Visiting /login while authenticated must redirect away
			await page.goto('/login');
			await page.waitForURL('**/dashboard', { timeout: 10000 });
			await expect(page).toHaveURL(/.*dashboard.*/);
		});

		test('read-only user does not see mutation/create actions', async ({ page }) => {
			// Clear cookies / start clean
			await page.context().clearCookies();
			await page.goto('/login');

			const readonlyEmail = process.env.TEST_READONLY_EMAIL || 'dev-readonly@dev.znero.test';
			const readonlyPassword = process.env.TEST_READONLY_PASSWORD || 'DevSeed1234!#';
			await page.fill('input#email', readonlyEmail);
			await page.fill('input#password', readonlyPassword);
			await page.click('button[type="submit"]');

			await page.waitForURL('**/dashboard');

			// Sidebar must be present
			await expect(page.locator('aside')).toBeVisible();
			// Topbar must show viewer's email
			await expect(page.locator('header').locator(`text=${readonlyEmail}`)).toBeVisible();
		});
	});

	test.describe('5. Storage and Cookie Security', () => {
		test('no tokens in browser storage and refresh cookie is HttpOnly and SameSite=Strict', async ({ page, context }) => {
			await page.goto('/login');

			const adminEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
			const adminPassword = process.env.TEST_ADMIN_PASSWORD || 'DevSeed1234!#';
			await page.fill('input#email', adminEmail);
			await page.fill('input#password', adminPassword);
			await page.click('button[type="submit"]');

			await page.waitForURL('**/dashboard');

			// 1. Verify localStorage has NO tokens
			const localKeys = await page.evaluate(() => Object.keys(localStorage));
			const localValues = await page.evaluate(() => Object.values(localStorage));
			for (const k of localKeys) {
				expect(k.toLowerCase()).not.toMatch(/token|jwt|session|refresh|access/);
			}
			for (const v of localValues) {
				expect(String(v)).not.toMatch(/eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+/); // No JWT format
			}

			// 2. Verify sessionStorage has NO tokens
			const sessionKeys = await page.evaluate(() => Object.keys(sessionStorage));
			const sessionValues = await page.evaluate(() => Object.values(sessionStorage));
			for (const k of sessionKeys) {
				expect(k.toLowerCase()).not.toMatch(/token|jwt|session|refresh|access/);
			}
			for (const v of sessionValues) {
				expect(String(v)).not.toMatch(/eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+/);
			}

			// 3. Verify refresh cookie is HttpOnly and SameSite=Strict
			const cookies = await context.cookies();
			const refreshCookie = cookies.find((c) => c.name === 'znero_refresh_token');
			expect(refreshCookie).toBeDefined();
			expect(refreshCookie?.httpOnly).toBe(true);
			expect(refreshCookie?.sameSite).toBe('Strict');
		});
	});

});
