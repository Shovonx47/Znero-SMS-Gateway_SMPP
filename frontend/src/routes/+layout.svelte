<script lang="ts">
	// Root layout — minimal shell.
	// Theme is initialized here so it applies to BOTH auth and app subtrees
	// (prevents flash of unstyled content in dark mode on the login page).
	// Route-group layouts handle everything else.
	import { onMount, type Snippet } from 'svelte';
	import { themeManager } from '$lib/theme.svelte';
	import './layout.css';

	interface Props {
		children: Snippet;
	}

	let { children }: Props = $props();

	onMount(() => {
		// themeManager initializes in its own constructor (reads localStorage).
		// Reference it here so the singleton is created during root-layout mount.
		void themeManager.current;
	});
</script>

{@render children()}
