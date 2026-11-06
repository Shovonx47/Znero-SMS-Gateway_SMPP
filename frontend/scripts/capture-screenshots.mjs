import { chromium } from '@playwright/test';
import fs from 'fs';
import path from 'path';

const ARTIFACT_DIR = 'C:\\Users\\asus\\.gemini\\antigravity\\brain\\c6630a2d-faec-4a9b-8f51-c4520398d8e0';
const LOCAL_DIR = 'e:\\Softcents Systems\\Znero International\\frontend\\screenshots';

fs.mkdirSync(LOCAL_DIR, { recursive: true });
fs.mkdirSync(ARTIFACT_DIR, { recursive: true });

async function main() {
	console.log('Launching Chromium...');
	const browser = await chromium.launch({ headless: true });
	const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	const page = await context.newPage();

	page.on('console', msg => console.log('PAGE LOG:', msg.text()));
	page.on('pageerror', err => console.log('PAGE ERROR:', err.message));

	// 1. Capture logged-out login page
	console.log('Navigating to http://127.0.0.1:5173/login...');
	await page.goto('http://127.0.0.1:5173/login');
	await page.waitForSelector('input#email', { state: 'visible' });

	const loginShotLocal = path.join(LOCAL_DIR, 'logged-out-login.png');
	const loginShotArtifact = path.join(ARTIFACT_DIR, 'logged-out-login.png');

	await page.screenshot({ path: loginShotLocal, fullPage: true });
	fs.copyFileSync(loginShotLocal, loginShotArtifact);
	console.log('Logged-out login screenshot saved to:', loginShotLocal, 'and', loginShotArtifact);

	// 2. Perform real login
	const testEmail = process.env.TEST_ADMIN_EMAIL || 'dev-superadmin@dev.znero.test';
	const testPassword = process.env.TEST_ADMIN_PASSWORD || 'DevSeed1234!#';

	console.log(`Filling login form with ${testEmail}...`);
	await page.fill('input#email', testEmail);
	await page.fill('input#password', testPassword);
	await page.click('button[type="submit"]');

	console.log('Waiting for navigation to dashboard...');
	await page.waitForURL('**/dashboard', { timeout: 15000 });
	await page.waitForSelector('aside', { state: 'visible', timeout: 10000 });

	// Allow health metrics and UI components to settle
	await page.waitForTimeout(2000);

	const appShotLocal = path.join(LOCAL_DIR, 'logged-in-dashboard.png');
	const appShotArtifact = path.join(ARTIFACT_DIR, 'logged-in-dashboard.png');

	await page.screenshot({ path: appShotLocal, fullPage: true });
	fs.copyFileSync(appShotLocal, appShotArtifact);
	console.log('Logged-in dashboard screenshot saved to:', appShotLocal, 'and', appShotArtifact);

	console.log('Testing navigation back to /login while authenticated...');
	await page.goto('http://127.0.0.1:5173/login');
	console.log('Current URL after page.goto(/login):', page.url());
	await page.waitForTimeout(2000);
	console.log('URL 2s after page.goto(/login):', page.url());

	await browser.close();
	console.log('Done capturing screenshots!');
}

main().catch((err) => {
	console.error('Screenshot capture failed:', err);
	process.exit(1);
});
