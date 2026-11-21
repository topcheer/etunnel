package server

// #240: the billing money paths had zero test coverage. These are the three
// requested regression tests - PayPal return entry guard, Stripe checkout
// without credentials, and billingFromEnv credential assembly.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 条目一：PayPal 激活入口守卫——无 token 或无 PayPal 配置时必须重定向回
// /pricing，绝不进入激活路径（#216/#218/#222 同区历史教训：钱路径不能静默）。
func TestHandlePayPalReturnGuardsEntry(t *testing.T) {
	s := &Server{} // 零值 billing：PayPalClientID 为空
	mk := func(url string) *http.Request {
		r := httptest.NewRequest("GET", url, nil)
		return r
	}
	rec := httptest.NewRecorder()
	s.handlePayPalReturn(rec, mk("https://t.example.com/billing/paypal/return"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/pricing" {
		t.Fatalf("no-token return must redirect to /pricing: code=%d loc=%q",
			rec.Code, rec.Header().Get("Location"))
	}
	rec2 := httptest.NewRecorder()
	s.handlePayPalReturn(rec2, mk("https://t.example.com/billing/paypal/return?token=ORDER123"))
	if rec2.Code != http.StatusSeeOther || rec2.Header().Get("Location") != "/pricing" {
		t.Fatalf("token-without-PayPal-config must redirect to /pricing: code=%d loc=%q",
			rec2.Code, rec2.Header().Get("Location"))
	}
}

// 条目二：Stripe 报价在无凭据时必须返回错误（不静默、不返回空 URL）。
func TestStripeCheckoutErrorsWithoutCredentials(t *testing.T) {
	s := &Server{billing: BillingConfig{}} // 无 StripeSecretKey
	if _, err := s.stripeCheckout("https://t.example.com", "buyer@example.com", "pro", "Pro", 900); err == nil {
		t.Fatalf("stripeCheckout without credentials must return an error")
	}
}

// 条目三：billingFromEnv 装配凭据 + PublicBaseURL 默认/覆盖语义。
func TestBillingFromEnvAssemblesCredentials(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_123")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_123")
	t.Setenv("PAYPAL_CLIENT_ID", "pid")
	t.Setenv("PAYPAL_SECRET", "psecret")
	t.Setenv("PAYPAL_ENV", "sandbox")
	t.Setenv("PAYPAL_WEBHOOK_ID", "pwhid")
	t.Setenv("NOWPAYMENTS_API_KEY", "npkey")
	t.Setenv("NOWPAYMENTS_IPNSecret", "") // 未设置
	t.Setenv("NOWPAYMENTS_IPN_SECRET", "npipn")
	cfg := billingFromEnv("t.example.com")
	if cfg.StripeSecretKey != "sk_test_123" || cfg.StripeWebhookSecret != "whsec_123" {
		t.Fatalf("stripe credentials not assembled: %+v", cfg)
	}
	if cfg.PayPalClientID != "pid" || cfg.PayPalSecret != "psecret" || cfg.PayPalEnv != "sandbox" || cfg.PayPalWebhookID != "pwhid" {
		t.Fatalf("paypal credentials not assembled: %+v", cfg)
	}
	if cfg.NowPaymentsAPIKey != "npkey" || cfg.NowPaymentsIPNSecret != "npipn" {
		t.Fatalf("nowpayments credentials not assembled: %+v", cfg)
	}
	if cfg.PublicBaseURL != "https://t.example.com" {
		t.Fatalf("PublicBaseURL default = https://<Domain>: %q", cfg.PublicBaseURL)
	}
	t.Setenv("ETUNNEL_PUBLIC_URL", "https://custom.example.org")
	cfg2 := billingFromEnv("t.example.com")
	if cfg2.PublicBaseURL != "https://custom.example.org" {
		t.Fatalf("ETUNNEL_PUBLIC_URL override ignored: %q", cfg2.PublicBaseURL)
	}
}
