/**
 * formatMoney.ts
 * Exact money formatting utilities.
 * NEVER uses floating-point rounding. Preserves fixed scale (scale 4 for telecom precision).
 */

const CURRENCY_SYMBOLS: Record<string, string> = {
	USD: '$',
	EUR: '€',
	GBP: '£',
	BDT: '৳',
	CAD: 'CA$',
	AUD: 'AU$'
};

export function getCurrencySymbol(currency: string = 'USD'): string {
	return CURRENCY_SYMBOLS[currency.toUpperCase()] || currency.toUpperCase();
}

/**
 * Formats a monetary string or number with thousands separators and fixed scale decimals.
 * Never performs silent mathematical rounding; preserves exact digits.
 */
export function formatMoney(
	val: string | number | null | undefined,
	currency: string = 'USD',
	decimals: number = 4
): string {
	if (val === null || val === undefined || val === '') {
		return `${getCurrencySymbol(currency)} 0.${'0'.repeat(decimals)}`;
	}

	const rawStr = typeof val === 'number' ? val.toString() : val.trim();
	const isNegative = rawStr.startsWith('-');
	const cleanStr = isNegative ? rawStr.slice(1) : rawStr;

	const parts = cleanStr.split('.');
	let integerPart = parts[0] || '0';
	let fractionalPart = parts[1] || '';

	// Add thousands separator commas
	integerPart = integerPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',');

	// Pad or truncate to exact fixed decimal scale without floating point errors
	if (fractionalPart.length < decimals) {
		fractionalPart = fractionalPart.padEnd(decimals, '0');
	} else if (fractionalPart.length > decimals) {
		fractionalPart = fractionalPart.slice(0, decimals);
	}

	const symbol = getCurrencySymbol(currency);
	const formatted = `${symbol} ${integerPart}.${fractionalPart}`;

	return isNegative ? `-${formatted}` : formatted;
}

export function isNegativeAmount(val: string | number | null | undefined): boolean {
	if (val === null || val === undefined) return false;
	const s = typeof val === 'number' ? val.toString() : val.trim();
	return s.startsWith('-') && !/^-\s*0(\.0+)?$/.test(s);
}

