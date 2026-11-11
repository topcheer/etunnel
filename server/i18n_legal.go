package server

// Legal-page chrome translations. The document bodies (terms, privacy) are
// English-only by design (the English version governs); these four keys
// localize the page chrome around them: tab titles, the "last updated" label,
// the governing-language note, plus reuse of the shared nav/foot keys.
//
// These keys are overlaid onto each language's dictionary at registration
// time (inside registerDict), so Go's file-order init sequencing cannot lose
// them, and TestI18nCompleteness still enforces presence in every language.

var legalKeys = map[string]map[string]string{
	"en": {
		"terms.title":   "Terms of Service",
		"privacy.title": "Privacy Policy",
		"legal.updated": "Last updated",
		"legal.en.note": "This document is provided in English; the English version governs.",
	},
	"zh-CN": {
		"terms.title":   "服务条款",
		"privacy.title": "隐私政策",
		"legal.updated": "最后更新",
		"legal.en.note": "本文件以英文提供，以英文版本为准。",
	},
	"ja": {
		"terms.title":   "利用規約",
		"privacy.title": "プライバシーポリシー",
		"legal.updated": "最終更新",
		"legal.en.note": "本文書は英語で提供されています。英語版が優先します。",
	},
	"ko": {
		"terms.title":   "서비스 이용약관",
		"privacy.title": "개인정보 처리방침",
		"legal.updated": "최종 업데이트",
		"legal.en.note": "이 문서는 영어로 제공되며 영어 버전이 우선합니다.",
	},
	"de": {
		"terms.title":   "Nutzungsbedingungen",
		"privacy.title": "Datenschutzerklärung",
		"legal.updated": "Zuletzt aktualisiert",
		"legal.en.note": "Dieses Dokument ist auf Englisch verfügbar; maßgeblich ist die englische Fassung.",
	},
	"fr": {
		"terms.title":   "Conditions d'utilisation",
		"privacy.title": "Politique de confidentialité",
		"legal.updated": "Dernière mise à jour",
		"legal.en.note": "Ce document est fourni en anglais ; la version anglaise prévaut.",
	},
	"es": {
		"terms.title":   "Términos del servicio",
		"privacy.title": "Política de privacidad",
		"legal.updated": "Última actualización",
		"legal.en.note": "Este documento se proporciona en inglés; rige la versión en inglés.",
	},
	"pt-BR": {
		"terms.title":   "Termos de serviço",
		"privacy.title": "Política de privacidade",
		"legal.updated": "Última atualização",
		"legal.en.note": "Este documento é fornecido em inglês; a versão em inglês prevalece.",
	},
	"it": {
		"terms.title":   "Termini di servizio",
		"privacy.title": "Informativa sulla privacy",
		"legal.updated": "Ultimo aggiornamento",
		"legal.en.note": "Questo documento è fornito in inglese; fa fede la versione inglese.",
	},
	"ru": {
		"terms.title":   "Условия использования",
		"privacy.title": "Политика конфиденциальности",
		"legal.updated": "Обновлено",
		"legal.en.note": "Документ предоставлен на английском языке; преимущественную силу имеет английская версия.",
	},
	"ar": {
		"terms.title":   "شروط الخدمة",
		"privacy.title": "سياسة الخصوصية",
		"legal.updated": "آخر تحديث",
		"legal.en.note": "يُقدَّم هذا المستند باللغة الإنجليزية؛ والنسخة الإنجليزية هي المعتمدة.",
	},
}
