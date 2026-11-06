import { browser } from '$app/environment';

export type Theme = 'light' | 'dark' | 'system';

class ThemeManager {
	current = $state<Theme>('system');
	resolvedDark = $state<boolean>(false);

	constructor() {
		if (browser) {
			const stored = localStorage.getItem('znero_theme') as Theme | null;
			if (stored === 'light' || stored === 'dark' || stored === 'system') {
				this.current = stored;
			}
			this.apply();

			window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
				if (this.current === 'system') {
					this.apply();
				}
			});
		}
	}

	set(theme: Theme) {
		this.current = theme;
		if (browser) {
			localStorage.setItem('znero_theme', theme);
			this.apply();
		}
	}

	toggle() {
		if (this.resolvedDark) {
			this.set('light');
		} else {
			this.set('dark');
		}
	}

	private apply() {
		if (!browser) return;
		const systemDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
		const isDark = this.current === 'dark' || (this.current === 'system' && systemDark);
		this.resolvedDark = isDark;
		if (isDark) {
			document.documentElement.classList.add('dark');
		} else {
			document.documentElement.classList.remove('dark');
		}
	}
}

export const themeManager = new ThemeManager();
