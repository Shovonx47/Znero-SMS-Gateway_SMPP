<script lang="ts">
	// Root index — redirects to /dashboard which is in the (app) route group.
	// The (app) layout guard handles authentication before rendering.
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';

	onMount(() => {
		goto('/dashboard', { replaceState: true });
	});
</script>
