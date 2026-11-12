// i18n.svelte.ts - Lightweight bilingual (en / bn) translation store for Phase 9 Visibility

export type Locale = 'en' | 'bn';

const translations = {
	en: {
		app_title: 'ZNERO International',
		nav_dashboard: 'Dashboard',
		nav_analytics: 'Analytics',
		nav_executive: 'Executive Overview',
		nav_noc: 'NOC & Live TPS',
		nav_reseller: 'Reseller Analytics',
		nav_customer: 'Customer Analytics',
		nav_alerts: 'Alert Rules',
		nav_incidents: 'Incident Center',
		nav_settings: 'Settings Center',
		nav_exports: 'Export Center',
		status_healthy: 'Operational',
		status_degraded: 'Degraded',
		status_critical: 'Critical',
		metric_submitted: 'Total Submitted',
		metric_delivered: 'Delivered',
		metric_failed: 'Failed',
		metric_rate: 'Delivery Rate',
		metric_revenue: 'Gross Revenue',
		metric_cost: 'Direct Buy Cost',
		metric_margin: 'Net Margin',
		metric_spend: 'Total Spend',
		metric_tps: 'Current TPS',
		metric_connections: 'Active Connections',
		filter_range: 'Date Range',
		filter_today: 'Today',
		filter_7d: 'Last 7 Days',
		filter_30d: 'Last 30 Days',
		filter_apply: 'Apply Filter',
		export_create: 'New Export Job',
		export_type: 'Report Type',
		export_format: 'Format',
		export_status: 'Status',
		export_rows: 'Rows',
		export_download: 'Download',
		search_placeholder: 'Search messages, customers, incidents, alerts...',
		settings_title: 'System & Tenant Settings',
		settings_save: 'Save Changes',
		settings_reset: 'Factory Reset',
		incident_declare: 'Declare Incident',
		incident_severity: 'Severity',
		incident_status: 'Status',
		incident_timeline: 'Activity Timeline',
		incident_resolve: 'Mark Resolved',
		theme_light: 'Light',
		theme_dark: 'Dark',
		theme_system: 'System',
		lang_en: 'English',
		lang_bn: 'বাংলা',
	},
	bn: {
		app_title: 'জেরো ইন্টারন্যাশনাল',
		nav_dashboard: 'ড্যাশবোর্ড',
		nav_analytics: 'অ্যানালিটিক্স',
		nav_executive: 'এক্সিকিউটিভ ওভারভিউ',
		nav_noc: 'এনওসি এবং লাইভ টিপিএস',
		nav_reseller: 'রিসেলার অ্যানালিটিক্স',
		nav_customer: 'গ্রাহক অ্যানালিটিক্স',
		nav_alerts: 'অ্যালার্ট রুলস',
		nav_incidents: 'ইনসিডেন্ট সেন্টার',
		nav_settings: 'সেটিংস সেন্টার',
		nav_exports: 'এক্সপোর্ট সেন্টার',
		status_healthy: 'সক্রিয় ও স্বাভাবিক',
		status_degraded: 'আংশিক ব্যাহত',
		status_critical: 'মারাত্মক সমস্যা',
		metric_submitted: 'মোট বার্তা প্রেরিত',
		metric_delivered: 'সফলভাবে ডেলিভার্ড',
		metric_failed: 'ব্যর্থ বার্তা',
		metric_rate: 'ডেলিভারি হার',
		metric_revenue: 'মোট আয়',
		metric_cost: 'ক্রয় খরচ',
		metric_margin: 'মুনাফা মার্জিন',
		metric_spend: 'মোট ব্যয়',
		metric_tps: 'বর্তমান টিপিএস',
		metric_connections: 'সক্রিয় সংযোগ',
		filter_range: 'তারিখ পরিসীমা',
		filter_today: 'আজকের তথ্য',
		filter_7d: 'বিগত ৭ দিন',
		filter_30d: 'বিগত ৩০ দিন',
		filter_apply: 'ফিল্টার প্রয়োগ করুন',
		export_create: 'নতুন এক্সপোর্ট তৈরি',
		export_type: 'রিপোর্টের ধরন',
		export_format: 'ফরম্যাট',
		export_status: 'স্ট্যাটাস',
		export_rows: 'সারি সংখ্যা',
		export_download: 'ডাউনলোড',
		search_placeholder: 'বার্তা, গ্রাহক, ইনসিডেন্ট, অ্যালার্ট অনুসন্ধান করুন...',
		settings_title: 'সিস্টেম এবং টেন্যান্ট সেটিংস',
		settings_save: 'সংরক্ষণ করুন',
		settings_reset: 'রিসেট করুন',
		incident_declare: 'ইনসিডেন্ট ঘোষণা করুন',
		incident_severity: 'তীব্রতা',
		incident_status: 'বর্তমান অবস্থা',
		incident_timeline: 'কার্যক্রম টাইমলাইন',
		incident_resolve: 'সমাধান চিহ্নিত করুন',
		theme_light: 'লাইট',
		theme_dark: 'ডার্ক',
		theme_system: 'সিস্টেম',
		lang_en: 'English',
		lang_bn: 'বাংলা',
	}
};

class I18nManager {
	current = $state<Locale>('en');

	constructor() {
		if (typeof window !== 'undefined') {
			const saved = localStorage.getItem('znero_locale') as Locale;
			if (saved === 'en' || saved === 'bn') {
				this.current = saved;
			}
		}
	}

	setLocale(locale: Locale) {
		this.current = locale;
		if (typeof window !== 'undefined') {
			localStorage.setItem('znero_locale', locale);
		}
	}

	t(key: keyof typeof translations['en']): string {
		const dict = translations[this.current] || translations.en;
		return (dict as any)[key] || translations.en[key] || key;
	}
}

export const i18n = new I18nManager();
