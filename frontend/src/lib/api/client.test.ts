import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ApiClient, ApiClientError } from './client';

describe('ApiClient', () => {
	let client: ApiClient;

	beforeEach(() => {
		client = new ApiClient('http://127.0.0.1:8080');
		client.setAccessToken(null);
		vi.restoreAllMocks();
	});

	it('parses standard success envelopes and returns data with requestId', async () => {
		const mockResponse = {
			success: true,
			data: { status: 'ok', version: 'v1.0.0', uptime_seconds: 42 },
			meta: { request_id: 'test-req-123' }
		};

		global.fetch = vi.fn().mockResolvedValue({
			ok: true,
			status: 200,
			headers: new Headers({ 'X-Request-ID': 'test-req-123' }),
			text: async () => JSON.stringify(mockResponse)
		} as Response);

		const result = await client.getHealth();
		expect(result.data.status).toBe('ok');
		expect(result.data.version).toBe('v1.0.0');
		expect(result.requestId).toBe('test-req-123');
	});

	it('attaches in-memory Bearer token to authenticated requests', async () => {
		client.setAccessToken('my-secret-in-memory-token');

		const mockResponse = {
			success: true,
			data: [],
			meta: { request_id: 'auth-req-1' }
		};

		const fetchMock = vi.fn().mockResolvedValue({
			ok: true,
			status: 200,
			headers: new Headers({ 'X-Request-ID': 'auth-req-1' }),
			text: async () => JSON.stringify(mockResponse)
		} as Response);

		global.fetch = fetchMock;

		await client.listRoles();

		expect(fetchMock).toHaveBeenCalledTimes(1);
		const callOptions = fetchMock.mock.calls[0][1] as RequestInit;
		const headers = callOptions.headers as Headers;
		expect(headers.get('Authorization')).toBe('Bearer my-secret-in-memory-token');
	});

	it('stores access token in memory upon login and clears it upon logout', async () => {
		const loginResponse = {
			success: true,
			data: {
				access_token: 'new-jwt-token-123',
				expires_in: 900
			},
			meta: { request_id: 'login-req' }
		};

		global.fetch = vi.fn().mockResolvedValue({
			ok: true,
			status: 200,
			headers: new Headers({ 'X-Request-ID': 'login-req' }),
			text: async () => JSON.stringify(loginResponse)
		} as Response);

		await client.login('admin@example.com', 'password');
		expect(client.getAccessToken()).toBe('new-jwt-token-123');

		// Logout clears memory
		global.fetch = vi.fn().mockResolvedValue({
			ok: true,
			status: 200,
			headers: new Headers(),
			text: async () => JSON.stringify({ success: true, data: {} })
		} as Response);

		await client.logout();
		expect(client.getAccessToken()).toBeNull();
	});

	it('unwraps 503 degraded readiness details as a partial report without throwing', async () => {
		const degradedResponse = {
			success: false,
			error: {
				code: 'not_ready',
				message: 'One or more dependencies are unavailable.',
				details: {
					status: 'down',
					checks: {
						database: { status: 'up', latency_ms: 1 },
						kafka: { status: 'down', latency_ms: 15, reason: 'unreachable' }
					},
					checked_at: '2026-11-03T00:00:00Z'
				}
			},
			meta: { request_id: 'degraded-req-456' }
		};

		global.fetch = vi.fn().mockResolvedValue({
			ok: false,
			status: 503,
			headers: new Headers({ 'X-Request-ID': 'degraded-req-456' }),
			text: async () => JSON.stringify(degradedResponse)
		} as Response);

		const result = await client.getReadiness();
		expect(result.isDegraded).toBe(true);
		expect(result.report.status).toBe('down');
		expect(result.report.checks.database.status).toBe('up');
		expect(result.report.checks.kafka.status).toBe('down');
		expect(result.requestId).toBe('degraded-req-456');
	});

	it('throws ApiClientError with code and details on API errors', async () => {
		const errorResponse = {
			success: false,
			error: {
				code: 'validation_failed',
				message: 'The message is invalid.',
				details: { message: 'message must not be empty' }
			},
			meta: { request_id: 'err-req-789' }
		};

		global.fetch = vi.fn().mockResolvedValue({
			ok: false,
			status: 422,
			headers: new Headers({ 'X-Request-ID': 'err-req-789' }),
			text: async () => JSON.stringify(errorResponse)
		} as Response);

		await expect(client.publishTestKafkaMessage('')).rejects.toThrow(ApiClientError);

		try {
			await client.publishTestKafkaMessage('');
		} catch (err) {
			const apiErr = err as ApiClientError;
			expect(apiErr.status).toBe(422);
			expect(apiErr.code).toBe('validation_failed');
			expect(apiErr.message).toBe('The message is invalid.');
			expect(apiErr.requestId).toBe('err-req-789');
		}
	});

	it('handles network failure gracefully as network_error code', async () => {
		global.fetch = vi.fn().mockRejectedValue(new Error('Connection refused'));

		try {
			await client.getHealth();
			expect.fail('Should have thrown an error');
		} catch (err) {
			const apiErr = err as ApiClientError;
			expect(apiErr.code).toBe('network_error');
			expect(apiErr.status).toBe(0);
			expect(apiErr.message).toContain('Unable to reach backend server');
		}
	});
});
