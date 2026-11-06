<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import { formatMoney } from '$lib/formatMoney';
	import type { Invoice, CustomerProfile } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let invoices = $state<Invoice[]>([]);
	let total = $state(0);
	let page = $state(1);
	let pageSize = $state(20);
	let search = $state('');
	let statusFilter = $state('');
	let loading = $state(true);
	let errorMessage = $state('');

	// Customer options for draft creation
	let customers = $state<Array<{ id: string; name: string }>>([]);

	// Create Draft Modal state
	let showCreateModal = $state(false);
	let createCustomerOrgID = $state('');
	let createCurrency = $state('USD');
	let createDueDate = $state('');
	let createTaxLabel = $state('');
	let createTaxAmount = $state('0.00');
	let createNotes = $state('');
	let createLines = $state<Array<{ description: string; quantity: string; unit_price: string }>>([
		{ description: '', quantity: '1', unit_price: '0.00' }
	]);
	let createSubmitting = $state(false);
	let createError = $state('');

	// Record Payment Modal state
	let showPaymentModal = $state(false);
	let paymentInvoice = $state<Invoice | null>(null);
	let paymentAmount = $state('');
	let paymentMethod = $state('Bank Wire');
	let paymentRef = $state('');
	let paymentNotes = $state('');
	let paymentSubmitting = $state(false);
	let paymentError = $state('');

	// Void Modal state
	let showVoidModal = $state(false);
	let voidInvoiceTarget = $state<Invoice | null>(null);
	let voidReason = $state('');
	let voidSubmitting = $state(false);
	let voidError = $state('');

	// Credit Note Modal state
	let showCreditNoteModal = $state(false);
	let creditNoteTarget = $state<Invoice | null>(null);
	let creditNoteReason = $state('');
	let creditNoteSubmitting = $state(false);
	let creditNoteError = $state('');

	const canCreateInvoice = $derived(auth.hasPermission('billing.invoice.create'));
	const canIssueInvoice = $derived(auth.hasPermission('billing.invoice.issue'));
	const canVoidInvoice = $derived(auth.hasPermission('billing.invoice.void'));
	const canCreditNote = $derived(auth.hasPermission('billing.invoice.creditnote'));
	const canRecordPayment = $derived(auth.hasPermission('billing.payment.create'));
	const isStaff = $derived(auth.tenant?.actor_org_type === 'platform');
	const isCustomer = $derived(auth.tenant?.actor_org_type === 'customer');

	async function loadInvoices() {
		loading = true;
		errorMessage = '';
		try {
			const res = await apiClient.listInvoices({
				search: search.trim() || undefined,
				status: statusFilter || undefined,
				page,
				page_size: pageSize
			});
			invoices = res.data.items || [];
			total = res.data.total;
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load invoices';
		} finally {
			loading = false;
		}
	}

	async function loadCustomers() {
		if (!isStaff) return;
		try {
			const res = await apiClient.listCustomerProfiles({ pageSize: 100 });
			customers = (res.data.items || []).map((cp: CustomerProfile) => ({
				id: cp.organization_id,
				name: cp.organization_name || 'Customer ' + cp.organization_id.slice(0, 8)
			}));
			if (customers.length > 0 && !createCustomerOrgID) {
				createCustomerOrgID = customers[0].id;
			}
		} catch (err) {
			console.error('Failed to load customers for invoice creation:', err);
		}
	}

	function addLine() {
		createLines = [...createLines, { description: '', quantity: '1', unit_price: '0.00' }];
	}

	function removeLine(idx: number) {
		if (createLines.length > 1) {
			createLines = createLines.filter((_, i) => i !== idx);
		}
	}

	async function handleCreateDraftSubmit(e: Event) {
		e.preventDefault();
		createError = '';
		if (!createCustomerOrgID) {
			createError = 'Customer organization is required.';
			return;
		}
		if (createLines.some((l) => !l.description.trim())) {
			createError = 'All line items must have a description.';
			return;
		}
		createSubmitting = true;
		try {
			await apiClient.createDraftInvoice({
				customer_org_id: createCustomerOrgID,
				currency: createCurrency,
				due_date: createDueDate ? new Date(createDueDate).toISOString() : undefined,
				tax_label: createTaxLabel.trim() || undefined,
				tax_amount: createTaxAmount || '0.00',
				notes: createNotes.trim() || undefined,
				lines: createLines
			});
			showCreateModal = false;
			createNotes = '';
			createLines = [{ description: '', quantity: '1', unit_price: '0.00' }];
			await loadInvoices();
		} catch (err: any) {
			createError = err.message || 'Failed to create draft invoice';
		} finally {
			createSubmitting = false;
		}
	}

	async function handleIssue(inv: Invoice) {
		if (!confirm(`Are you sure you want to issue this draft invoice? Issuing assigns a sequential, gap-free invoice number and locks financial terms.`)) {
			return;
		}
		try {
			await apiClient.issueInvoice(inv.id);
			await loadInvoices();
		} catch (err: any) {
			alert('Issue failed: ' + (err.message || 'Unknown error'));
		}
	}

	async function handlePaymentSubmit(e: Event) {
		e.preventDefault();
		if (!paymentInvoice || !paymentAmount) return;
		paymentSubmitting = true;
		paymentError = '';
		try {
			await apiClient.recordPayment(paymentInvoice.id, {
				amount: paymentAmount,
				payment_method: paymentMethod.trim(),
				reference: paymentRef.trim() || undefined,
				notes: paymentNotes.trim() || undefined
			});
			showPaymentModal = false;
			paymentAmount = '';
			paymentRef = '';
			paymentNotes = '';
			await loadInvoices();
		} catch (err: any) {
			paymentError = err.message || 'Failed to record payment';
		} finally {
			paymentSubmitting = false;
		}
	}

	async function handleVoidSubmit(e: Event) {
		e.preventDefault();
		if (!voidInvoiceTarget || !voidReason.trim()) return;
		voidSubmitting = true;
		voidError = '';
		try {
			const res = await apiClient.voidInvoice(voidInvoiceTarget.id, voidReason.trim());
			showVoidModal = false;
			voidReason = '';
			if (res.data.pending_approval) {
				alert('Void request submitted for Maker-Checker approval.');
			} else {
				alert('Invoice successfully voided.');
			}
			await loadInvoices();
		} catch (err: any) {
			voidError = err.message || 'Failed to void invoice';
		} finally {
			voidSubmitting = false;
		}
	}

	async function handleCreditNoteSubmit(e: Event) {
		e.preventDefault();
		if (!creditNoteTarget || !creditNoteReason.trim()) return;
		creditNoteSubmitting = true;
		creditNoteError = '';
		try {
			await apiClient.createCreditNote(creditNoteTarget.id, creditNoteReason.trim());
			showCreditNoteModal = false;
			creditNoteReason = '';
			await loadInvoices();
		} catch (err: any) {
			creditNoteError = err.message || 'Failed to issue credit note';
		} finally {
			creditNoteSubmitting = false;
		}
	}

	function getStatusBadge(status: string) {
		switch (status) {
			case 'draft':
				return 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300';
			case 'issued':
				return 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300';
			case 'paid':
				return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300';
			case 'void':
				return 'bg-rose-100 text-rose-800 dark:bg-rose-950 dark:text-rose-300';
			default:
				return 'bg-slate-100 text-slate-800';
		}
	}

	onMount(() => {
		loadInvoices();
		loadCustomers();
	});
</script>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				{isCustomer ? 'My Invoices' : 'Invoices & Billing'}
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				{isCustomer
					? 'View and download customer billing statements and issued tax invoices.'
					: 'Manage customer invoices, sequential numbering, manual payments, and credit notes.'}
			</p>
		</div>

		{#if canCreateInvoice}
			<button
				onclick={() => (showCreateModal = true)}
				class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
			>
				+ Create Draft Invoice
			</button>
		{/if}
	</div>

	<!-- Filter Controls -->
	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<div class="flex flex-wrap items-center gap-3">
			<div class="relative min-w-[240px] flex-1">
				<input
					type="text"
					bind:value={search}
					onkeydown={(e) => e.key === 'Enter' && (page = 1, loadInvoices())}
					placeholder="Search invoice number or notes..."
					class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 pl-9 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
				<svg class="absolute left-3 top-2.5 h-4 w-4 text-slate-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
				</svg>
			</div>

			<div>
				<select
					bind:value={statusFilter}
					onchange={() => { page = 1; loadInvoices(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Statuses</option>
					<option value="draft">Draft</option>
					<option value="issued">Issued</option>
					<option value="paid">Paid</option>
					<option value="void">Void</option>
				</select>
			</div>

			<button
				onclick={() => { page = 1; loadInvoices(); }}
				class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
			>
				Filter
			</button>
		</div>
	</div>

	<!-- Invoices Table -->
	{#if loading}
		<Loading message="Loading invoices..." />
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadInvoices} />
	{:else if invoices.length === 0}
		<Empty description="No invoices found." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Invoice #</th>
							<th class="px-4 py-3">Customer</th>
							<th class="px-4 py-3">Issue Date</th>
							<th class="px-4 py-3">Due Date</th>
							<th class="px-4 py-3 text-right">Subtotal</th>
							<th class="px-4 py-3 text-right">Tax</th>
							<th class="px-4 py-3 text-right">Total</th>
							<th class="px-4 py-3 text-right">Paid</th>
							<th class="px-4 py-3 text-center">Status</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each invoices as inv (inv.id)}
							<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
								<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
									{#if inv.invoice_number}
										<a href="/invoices/{inv.id}" class="text-indigo-600 hover:underline dark:text-indigo-400">
											{inv.invoice_number}
										</a>
									{:else}
										<span class="text-slate-400 italic">DRAFT</span>
									{/if}
									{#if inv.credit_note_for_invoice_number}
										<div class="text-[10px] font-normal text-amber-600">
											For: {inv.credit_note_for_invoice_number}
										</div>
									{/if}
								</td>
								<td class="px-4 py-3 text-slate-800 dark:text-slate-200">
									{inv.customer_org_name || inv.customer_org_id.slice(0, 8)}
								</td>
								<td class="px-4 py-3 text-slate-500 whitespace-nowrap">
									{inv.issue_date ? new Date(inv.issue_date).toLocaleDateString() : '-'}
								</td>
								<td class="px-4 py-3 text-slate-500 whitespace-nowrap">
									{inv.due_date ? new Date(inv.due_date).toLocaleDateString() : '-'}
								</td>
								<td class="px-4 py-3 text-right font-mono text-slate-600 dark:text-slate-300">
									{formatMoney(inv.subtotal, inv.currency)}
								</td>
								<td class="px-4 py-3 text-right font-mono text-slate-500">
									{formatMoney(inv.tax_amount, inv.currency)}
								</td>
								<td class="px-4 py-3 text-right font-mono font-bold text-slate-900 dark:text-white">
									{formatMoney(inv.total, inv.currency)}
								</td>
								<td class="px-4 py-3 text-right font-mono text-emerald-600 font-semibold">
									{formatMoney(inv.amount_paid, inv.currency)}
								</td>
								<td class="px-4 py-3 text-center">
									<span class="inline-block rounded-full px-2.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider {getStatusBadge(inv.status)}">
										{inv.status}
									</span>
								</td>
								<td class="px-4 py-3 text-right whitespace-nowrap space-x-1">
									<a
										href="/invoices/{inv.id}"
										class="rounded bg-slate-100 px-2 py-1 font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300"
									>
										View
									</a>

									{#if isStaff}
										{#if inv.status === 'draft' && canIssueInvoice}
											<button
												onclick={() => handleIssue(inv)}
												class="rounded bg-indigo-600 px-2 py-1 font-semibold text-white hover:bg-indigo-700"
											>
												Issue
											</button>
										{/if}

										{#if inv.status === 'issued' && canRecordPayment}
											<button
												onclick={() => { paymentInvoice = inv; paymentAmount = inv.outstanding_balance; showPaymentModal = true; }}
												class="rounded bg-emerald-600 px-2 py-1 font-semibold text-white hover:bg-emerald-700"
											>
												Pay
											</button>
										{/if}

										{#if (inv.status === 'issued' || inv.status === 'paid') && canCreditNote}
											<button
												onclick={() => { creditNoteTarget = inv; showCreditNoteModal = true; }}
												class="rounded border border-amber-300 bg-amber-50 px-2 py-1 font-semibold text-amber-800 hover:bg-amber-100 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-300"
											>
												Credit Note
											</button>
										{/if}

										{#if inv.status === 'issued' && canVoidInvoice}
											<button
												onclick={() => { voidInvoiceTarget = inv; showVoidModal = true; }}
												class="rounded border border-rose-300 bg-rose-50 px-2 py-1 font-semibold text-rose-800 hover:bg-rose-100 dark:border-rose-700 dark:bg-rose-950 dark:text-rose-300"
											>
												Void
											</button>
										{/if}
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>

			<!-- Pagination -->
			{#if total > pageSize}
				<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
					<div class="text-xs text-slate-500">
						Showing {(page - 1) * pageSize + 1} to {Math.min(page * pageSize, total)} of {total} invoices
					</div>
					<div class="flex gap-2">
						<button
							disabled={page === 1}
							onclick={() => { page--; loadInvoices(); }}
							class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 disabled:opacity-50 dark:border-slate-700 dark:text-slate-400"
						>
							Previous
						</button>
						<button
							disabled={page * pageSize >= total}
							onclick={() => { page++; loadInvoices(); }}
							class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 disabled:opacity-50 dark:border-slate-700 dark:text-slate-400"
						>
							Next
						</button>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>

<!-- Modal: Create Draft Invoice -->
{#if showCreateModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 overflow-y-auto">
		<div class="w-full max-w-2xl rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 my-8">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Create New Draft Invoice</h2>
			<p class="mt-1 text-xs text-slate-500">Draft invoices are editable until issued.</p>

			<form onsubmit={handleCreateDraftSubmit} class="mt-4 space-y-4 text-xs">
				<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
					<div>
						<label for="create-cust" class="block font-medium text-slate-700 dark:text-slate-300">Customer *</label>
						<select
							id="create-cust"
							required
							bind:value={createCustomerOrgID}
							class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							{#each customers as c}
								<option value={c.id}>{c.name}</option>
							{/each}
						</select>
					</div>

					<div>
						<label for="create-curr" class="block font-medium text-slate-700 dark:text-slate-300">Currency *</label>
						<select
							id="create-curr"
							bind:value={createCurrency}
							class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value="USD">USD ($)</option>
							<option value="EUR">EUR (€)</option>
						</select>
					</div>

					<div>
						<label for="create-due" class="block font-medium text-slate-700 dark:text-slate-300">Due Date</label>
						<input
							id="create-due"
							type="date"
							bind:value={createDueDate}
							class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				<!-- Line Items -->
				<div class="space-y-2 border-t border-slate-200 pt-3 dark:border-slate-800">
					<div class="flex items-center justify-between">
						<span class="font-bold text-slate-900 dark:text-white">Invoice Lines</span>
						<button
							type="button"
							onclick={addLine}
							class="rounded bg-slate-100 px-2 py-1 font-semibold text-indigo-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-indigo-400"
						>
							+ Add Line
						</button>
					</div>

					{#each createLines as line, idx}
						<div class="flex items-center gap-2">
							<div class="flex-1">
								<input
									type="text"
									required
									bind:value={line.description}
									placeholder="Line description..."
									class="w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
								/>
							</div>
							<div class="w-24">
								<input
									type="number"
									step="1"
									min="1"
									required
									bind:value={line.quantity}
									placeholder="Qty"
									class="w-full rounded-lg border border-slate-300 p-2 text-right font-mono text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
								/>
							</div>
							<div class="w-28">
								<input
									type="number"
									step="0.0001"
									min="0"
									required
									bind:value={line.unit_price}
									placeholder="Unit Price"
									class="w-full rounded-lg border border-slate-300 p-2 text-right font-mono text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
								/>
							</div>
							<button
								type="button"
								onclick={() => removeLine(idx)}
								class="rounded p-1 text-slate-400 hover:text-rose-600"
								title="Remove line"
							>
								&times;
							</button>
						</div>
					{/each}
				</div>

				<!-- Tax & Notes -->
				<div class="grid grid-cols-1 gap-4 sm:grid-cols-2 border-t border-slate-200 pt-3 dark:border-slate-800">
					<div>
						<label for="create-taxlbl" class="block font-medium text-slate-700 dark:text-slate-300">Tax Label (Optional)</label>
						<input
							id="create-taxlbl"
							type="text"
							bind:value={createTaxLabel}
							placeholder="e.g. VAT 10%"
							class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
					<div>
						<label for="create-taxamt" class="block font-medium text-slate-700 dark:text-slate-300">Tax Amount</label>
						<input
							id="create-taxamt"
							type="number"
							step="0.0001"
							bind:value={createTaxAmount}
							placeholder="0.00"
							class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-right font-mono text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label for="create-notes" class="block font-medium text-slate-700 dark:text-slate-300">Notes / Payment Terms</label>
					<textarea
						id="create-notes"
						rows="2"
						bind:value={createNotes}
						placeholder="e.g. Bank transfer payable within 30 days."
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>

				{#if createError}
					<div class="rounded bg-rose-50 p-2 text-xs font-semibold text-rose-700 dark:bg-rose-950 dark:text-rose-300">
						{createError}
					</div>
				{/if}

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showCreateModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={createSubmitting}
						class="rounded-lg bg-indigo-600 px-4 py-2 font-semibold text-white hover:bg-indigo-700 disabled:opacity-50"
					>
						{createSubmitting ? 'Saving...' : 'Save Draft'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Modal: Record Payment -->
{#if showPaymentModal && paymentInvoice}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
		<div class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Record Manual Payment</h2>
			<p class="mt-1 text-xs text-slate-500">
				Invoice: <span class="font-bold">{paymentInvoice.invoice_number}</span> &bull; Outstanding: <span class="font-bold">{formatMoney(paymentInvoice.outstanding_balance, paymentInvoice.currency)}</span>
			</p>

			<form onsubmit={handlePaymentSubmit} class="mt-4 space-y-4 text-xs">
				<div>
					<label for="pmt-amt" class="block font-medium text-slate-700 dark:text-slate-300">Payment Amount ({paymentInvoice.currency}) *</label>
					<input
						id="pmt-amt"
						type="number"
						step="0.0001"
						min="0.0001"
						required
						bind:value={paymentAmount}
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 font-mono text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>
				<div>
					<label for="pmt-method" class="block font-medium text-slate-700 dark:text-slate-300">Payment Method (Free text) *</label>
					<input
						id="pmt-method"
						type="text"
						required
						bind:value={paymentMethod}
						placeholder="e.g. Bank Wire, Stripe Transfer, Cheque"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>
				<div>
					<label for="pmt-ref" class="block font-medium text-slate-700 dark:text-slate-300">External Payment Reference</label>
					<input
						id="pmt-ref"
						type="text"
						bind:value={paymentRef}
						placeholder="e.g. WIRE-998811-CHASE"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>
				<div>
					<label for="pmt-notes" class="block font-medium text-slate-700 dark:text-slate-300">Notes (Optional)</label>
					<textarea
						id="pmt-notes"
						rows="2"
						bind:value={paymentNotes}
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>

				{#if paymentError}
					<div class="rounded bg-rose-50 p-2 text-xs font-semibold text-rose-700 dark:bg-rose-950 dark:text-rose-300">
						{paymentError}
					</div>
				{/if}

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showPaymentModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={paymentSubmitting}
						class="rounded-lg bg-emerald-600 px-4 py-2 font-semibold text-white hover:bg-emerald-700 disabled:opacity-50"
					>
						{paymentSubmitting ? 'Posting...' : 'Record Payment'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Modal: Void Invoice -->
{#if showVoidModal && voidInvoiceTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
		<div class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Void Issued Invoice</h2>
			<p class="mt-1 text-xs text-slate-500">
				Voiding invoice <span class="font-bold">{voidInvoiceTarget.invoice_number}</span> requires mandatory justification.
			</p>

			<form onsubmit={handleVoidSubmit} class="mt-4 space-y-4 text-xs">
				<div>
					<label for="void-reason" class="block font-medium text-slate-700 dark:text-slate-300">Mandatory Void Reason *</label>
					<textarea
						id="void-reason"
						required
						rows="3"
						bind:value={voidReason}
						placeholder="e.g. Duplicate document issued by mistake; replacement issued under INV-..."
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>

				{#if voidError}
					<div class="rounded bg-rose-50 p-2 text-xs font-semibold text-rose-700 dark:bg-rose-950 dark:text-rose-300">
						{voidError}
					</div>
				{/if}

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showVoidModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={voidSubmitting}
						class="rounded-lg bg-rose-600 px-4 py-2 font-semibold text-white hover:bg-rose-700 disabled:opacity-50"
					>
						{voidSubmitting ? 'Voiding...' : 'Confirm Void'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Modal: Credit Note -->
{#if showCreditNoteModal && creditNoteTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
		<div class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Issue Credit Note</h2>
			<p class="mt-1 text-xs text-slate-500">
				Creates a credit document reversing invoice <span class="font-bold">{creditNoteTarget.invoice_number}</span>.
			</p>

			<form onsubmit={handleCreditNoteSubmit} class="mt-4 space-y-4 text-xs">
				<div>
					<label for="cn-reason" class="block font-medium text-slate-700 dark:text-slate-300">Mandatory Credit Reason *</label>
					<textarea
						id="cn-reason"
						required
						rows="3"
						bind:value={creditNoteReason}
						placeholder="e.g. Service level agreement credit following outage investigation"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>

				{#if creditNoteError}
					<div class="rounded bg-rose-50 p-2 text-xs font-semibold text-rose-700 dark:bg-rose-950 dark:text-rose-300">
						{creditNoteError}
					</div>
				{/if}

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showCreditNoteModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={creditNoteSubmitting}
						class="rounded-lg bg-amber-600 px-4 py-2 font-semibold text-white hover:bg-amber-700 disabled:opacity-50"
					>
						{creditNoteSubmitting ? 'Issuing...' : 'Issue Credit Note'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
