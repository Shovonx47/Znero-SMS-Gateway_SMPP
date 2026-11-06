import { defineConfig } from 'vitest/config';
import tailwindcss from '@tailwindcss/vite';
import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';

const apiPort = process.env.APP_PORT || '8080';
const apiTarget = `http://127.0.0.1:${apiPort}`;

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			adapter: adapter()
		})
	],
	server: {
		host: '127.0.0.1',
		port: 5173,
		proxy: {
			'/api': {
				target: apiTarget,
				changeOrigin: true,
				configure: (proxy) => {
					proxy.on('proxyReq', (proxyReq) => {
						proxyReq.removeHeader('origin');
						proxyReq.removeHeader('referer');
					});
				}
			},
			'/healthz': {
				target: apiTarget,
				changeOrigin: true
			},
			'/readyz': {
				target: apiTarget,
				changeOrigin: true
			}
		}
	},
	test: {
		expect: { requireAssertions: true },
		environment: 'node',
		include: ['src/**/*.{test,spec}.{js,ts}']
	}
});
