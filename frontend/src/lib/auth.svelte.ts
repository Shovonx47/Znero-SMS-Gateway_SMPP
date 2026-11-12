import { browser } from '$app/environment';
import { apiClient } from './api/client';
import type { TenantContext, User } from './types/api';

class AuthManager {
	user = $state<User | null>(null);
	tenant = $state<TenantContext | null>(null);
	loading = $state<boolean>(true);
	initialized = $state<boolean>(false);

	get isAuthenticated(): boolean {
		return !!this.user;
	}

	get isPlatformAdmin(): boolean {
		return !!this.tenant?.is_platform_admin;
	}

	async init() {
		if (!browser) return;
		this.loading = true;
		try {
			// If no access token in memory, attempt silent refresh via httpOnly cookie
			if (!apiClient.getAccessToken()) {
				await apiClient.refreshToken();
			}
			const meRes = await apiClient.getMe();
			this.user = meRes.data.user;
			
			// Normalize tenant context
			const rawTenant = meRes.data.tenant_context as any;
			if (rawTenant) {
				this.tenant = {
					actor_user_id: rawTenant.actor_user_id || rawTenant.ActorUserID,
					actor_org_id: rawTenant.actor_org_id || rawTenant.ActorOrgID,
					actor_org_type: rawTenant.actor_org_type || rawTenant.ActorOrgType,
					actor_org_path: rawTenant.actor_org_path || rawTenant.ActorOrgPath,
					is_platform_admin: !!(rawTenant.is_platform_admin || rawTenant.IsPlatformAdmin),
					permissions: rawTenant.permissions || rawTenant.Permissions || {}
				};
			} else {
				this.tenant = null;
			}
		} catch {
			this.user = null;
			this.tenant = null;
			apiClient.setAccessToken(null);
		} finally {
			this.loading = false;
			this.initialized = true;
		}
	}

	setSession(user: User, tenant: TenantContext) {
		this.user = user;
		this.tenant = tenant;
		this.loading = false;
		this.initialized = true;
	}

	async logout() {
		try {
			await apiClient.logout();
		} catch {
			// ignore logout network errors
		} finally {
			this.user = null;
			this.tenant = null;
			apiClient.setAccessToken(null);
		}
	}

	hasPermission(permCode: string): boolean {
		if (!this.tenant) return false;
		if (this.tenant.is_platform_admin) return true;
		const perms = this.tenant.permissions;
		if (!perms) return false;
		// Check exact code, dot replacement, or colon replacement
		return !!(perms[permCode] || perms[permCode.replace(':', '.')]);
	}
}

export const auth = new AuthManager();
