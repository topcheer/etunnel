package server

// Debounced subdomain-availability strings for the /me page (overlay-registered
// like the legal chrome keys; see i18n_legal.go).

var domKeys = map[string]map[string]string{
	"en": {
		"me.dom.checking": "checking...",
		"me.dom.taken":    "already taken",
		"me.dom.free":     "available",
		"me.dom.invalid":  "invalid subdomain (1-63 chars, a-z 0-9 -)",
	},
	"zh-CN": {
		"me.dom.checking": "检查中...",
		"me.dom.taken":    "已被占用",
		"me.dom.free":     "可用",
		"me.dom.invalid":  "无效子域（1-63 字符，a-z 0-9 -）",
	},
	"ja": {
		"me.dom.checking": "確認中...",
		"me.dom.taken":    "使用済み",
		"me.dom.free":     "利用可能",
		"me.dom.invalid":  "無効なサブドメイン（1-63文字、a-z 0-9 -）",
	},
	"ko": {
		"me.dom.checking": "확인 중...",
		"me.dom.taken":    "이미 사용 중",
		"me.dom.free":     "사용 가능",
		"me.dom.invalid":  "잘못된 서브도메인 (1-63자, a-z 0-9 -)",
	},
	"de": {
		"me.dom.checking": "wird geprüft...",
		"me.dom.taken":    "bereits vergeben",
		"me.dom.free":     "verfügbar",
		"me.dom.invalid":  "ungültige Subdomain (1-63 Zeichen, a-z 0-9 -)",
	},
	"fr": {
		"me.dom.checking": "vérification...",
		"me.dom.taken":    "déjà pris",
		"me.dom.free":     "disponible",
		"me.dom.invalid":  "sous-domaine invalide (1-63 caractères, a-z 0-9 -)",
	},
	"es": {
		"me.dom.checking": "comprobando...",
		"me.dom.taken":    "ya está en uso",
		"me.dom.free":     "disponible",
		"me.dom.invalid":  "subdominio no válido (1-63 caracteres, a-z 0-9 -)",
	},
	"pt-BR": {
		"me.dom.checking": "verificando...",
		"me.dom.taken":    "já em uso",
		"me.dom.free":     "disponível",
		"me.dom.invalid":  "subdomínio inválido (1-63 caracteres, a-z 0-9 -)",
	},
	"it": {
		"me.dom.checking": "verifica...",
		"me.dom.taken":    "già in uso",
		"me.dom.free":     "disponibile",
		"me.dom.invalid":  "sottodominio non valido (1-63 caratteri, a-z 0-9 -)",
	},
	"ru": {
		"me.dom.checking": "проверка...",
		"me.dom.taken":    "уже занят",
		"me.dom.free":     "свободен",
		"me.dom.invalid":  "недопустимый поддомен (1-63 символа, a-z 0-9 -)",
	},
	"ar": {
		"me.dom.checking": "جارٍ التحقق...",
		"me.dom.taken":    "مأخوذ بالفعل",
		"me.dom.free":     "متاح",
		"me.dom.invalid":  "نطاق فرعي غير صالح (1-63 حرفًا، a-z 0-9 -)",
	},
}
