<!--
  Toast.svelte
  Simple toast notification system.
  Usage: import { toastStore, toast } from '$lib/components/Toast.svelte'
-->
<script lang="ts" module>
	import { writable } from 'svelte/store';

	export type ToastType = 'success' | 'error' | 'info' | 'warning';

	export interface ToastItem {
		id: number;
		type: ToastType;
		message: string;
		duration?: number;
	}

	let _id = 0;
	export const toastStore = writable<ToastItem[]>([]);

	export interface ToastFn {
		(message: string, type?: ToastType, duration?: number): void;
		success(message: string, duration?: number): void;
		error(message: string, duration?: number): void;
		info(message: string, duration?: number): void;
		warning(message: string, duration?: number): void;
	}

	function _toast(message: string, type: ToastType = 'info', duration = 4000) {
		const id = ++_id;
		toastStore.update((t) => [...t, { id, type, message, duration }]);
		if (duration > 0) {
			setTimeout(() => dismiss(id), duration);
		}
	}

	_toast.success = (message: string, duration?: number) => _toast(message, 'success', duration);
	_toast.error = (message: string, duration?: number) => _toast(message, 'error', duration);
	_toast.info = (message: string, duration?: number) => _toast(message, 'info', duration);
	_toast.warning = (message: string, duration?: number) => _toast(message, 'warning', duration);

	export const toast: ToastFn = _toast;

	export function dismiss(id: number) {
		toastStore.update((t) => t.filter((item) => item.id !== id));
	}
</script>

<script lang="ts">
	const colorMap: Record<string, string> = {
		success: 'bg-emerald-50 border-emerald-200 text-emerald-800 dark:bg-emerald-950/50 dark:border-emerald-800 dark:text-emerald-200',
		error: 'bg-rose-50 border-rose-200 text-rose-800 dark:bg-rose-950/50 dark:border-rose-800 dark:text-rose-200',
		warning: 'bg-amber-50 border-amber-200 text-amber-800 dark:bg-amber-950/50 dark:border-amber-800 dark:text-amber-200',
		info: 'bg-blue-50 border-blue-200 text-blue-800 dark:bg-blue-950/50 dark:border-blue-800 dark:text-blue-200'
	};

	const iconMap: Record<string, string> = {
		success: 'M5 13l4 4L19 7',
		error: 'M6 18L18 6M6 6l12 12',
		warning: 'M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z',
		info: 'M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z'
	};
</script>

<div class="fixed bottom-4 right-4 z-[9999] flex flex-col gap-2 max-w-sm w-full pointer-events-none">
	{#each $toastStore as item (item.id)}
		<div
			class="pointer-events-auto flex items-start gap-3 rounded-xl border px-4 py-3 shadow-lg text-xs font-medium {colorMap[item.type]}"
			role="alert"
		>
			<svg class="mt-0.5 h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
				<path stroke-linecap="round" stroke-linejoin="round" d={iconMap[item.type]} />
			</svg>
			<span class="flex-1">{item.message}</span>
			<button
				type="button"
				onclick={() => dismiss(item.id)}
				class="shrink-0 opacity-60 hover:opacity-100 transition"
				aria-label="Dismiss"
			>
				<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
					<path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
				</svg>
			</button>
		</div>
	{/each}
</div>
