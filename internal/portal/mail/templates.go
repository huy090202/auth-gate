package mail

type tpl struct{ subject, body string }

// Kinds lists every email kind the Portal sends.
var Kinds = []string{
	"verify", "signup-exists", "reset", "factor-reset-confirm", "factor-reset-started",
	"email-change-confirm", "email-changed", "password-changed", "factor-added",
	"factor-removed", "recovery-code-used", "account-locked", "account-deleted",
}

// linkKinds are the kinds whose whole point is a link.
var linkKinds = map[string]bool{"verify": true, "reset": true, "factor-reset-confirm": true, "email-change-confirm": true}

var untilLabel = map[string]string{"en": "Until: ", "vi": "Cho đến: "}

// buttons are the primary button labels of the link kinds.
var buttons = map[string]map[string]string{
	"verify":               {"en": "Confirm email address", "vi": "Xác nhận email"},
	"reset":                {"en": "Choose a new password", "vi": "Chọn mật khẩu mới"},
	"factor-reset-confirm": {"en": "Confirm the reset", "vi": "Xác nhận đặt lại"},
	"email-change-confirm": {"en": "Confirm new address", "vi": "Xác nhận địa chỉ mới"},
}

var signInLabel = map[string]string{"en": "Sign in", "vi": "Đăng nhập"}

var fallbackLine = map[string]string{
	"en": "Button not working? Paste this link into your browser:",
	"vi": "Nút không bấm được? Dán liên kết này vào trình duyệt:",
}

var linkFooter = map[string]string{
	"en": "If you did not ask for this, ignore this email; nothing changes until the link is opened.",
	"vi": "Nếu bạn không yêu cầu, hãy bỏ qua thư này; không có gì thay đổi cho tới khi liên kết được mở.",
}

// noticeFooter has one %s for the support address; noticeFooterBare is used without one.
var noticeFooter = map[string]string{
	"en": "If this was not you, change your password now. If you cannot sign in, write to %s.",
	"vi": "Nếu không phải bạn, hãy đổi mật khẩu ngay. Nếu không đăng nhập được, hãy viết cho %s.",
}

var noticeFooterBare = map[string]string{
	"en": "If this was not you, change your password now. If you cannot sign in, contact the operator who runs this service.",
	"vi": "Nếu không phải bạn, hãy đổi mật khẩu ngay. Nếu không đăng nhập được, hãy liên hệ người vận hành dịch vụ này.",
}

// supportLine has one %s for the support address.
var supportLine = map[string]string{
	"en": "Questions? Write to %s.",
	"vi": "Cần hỗ trợ? Viết cho %s.",
}

// whyLine has one %s for the host of the Portal.
var whyLine = map[string]string{
	"en": "AuthGate · %s · Sent because this address was used on the AuthGate Portal.",
	"vi": "AuthGate · %s · Thư này gửi đến bạn vì địa chỉ này được dùng trên AuthGate Portal.",
}

var templates = map[string]map[string]tpl{
	"verify": {
		"en": {"Confirm your email address", "Open this link within 24 hours to confirm your email address and finish creating your account:"},
		"vi": {"Xác nhận địa chỉ email của bạn", "Hãy mở liên kết này trong vòng 24 giờ để xác nhận địa chỉ email và hoàn tất việc tạo tài khoản:"},
	},
	"signup-exists": {
		"en": {"You already have an account", "Someone tried to create an account with this email address, but it already has one. You can sign in, or reset your password from the sign-in page."},
		"vi": {"Bạn đã có tài khoản", "Có người vừa thử tạo tài khoản với địa chỉ email này, nhưng địa chỉ này đã có tài khoản. Bạn có thể đăng nhập, hoặc đặt lại mật khẩu từ trang đăng nhập."},
	},
	"reset": {
		"en": {"Reset your password", "Open this link within 1 hour to choose a new password:"},
		"vi": {"Đặt lại mật khẩu", "Hãy mở liên kết này trong vòng 1 giờ để chọn mật khẩu mới:"},
	},
	"factor-reset-confirm": {
		"en": {"Confirm the reset of your sign-in factors", "Open this link within 1 hour to confirm that you want to reset your PIN and passkeys. After you confirm, a 24 hour wait starts before the reset takes effect:"},
		"vi": {"Xác nhận đặt lại các yếu tố đăng nhập", "Hãy mở liên kết này trong vòng 1 giờ để xác nhận bạn muốn đặt lại mã PIN và khóa truy cập. Sau khi xác nhận, sẽ có 24 giờ chờ trước khi việc đặt lại có hiệu lực:"},
	},
	"factor-reset-started": {
		"en": {"Your sign-in factors will be reset", "The reset of your PIN and passkeys is waiting out its 24 hour delay. Until then you can still sign in with your current factors, which cancels the reset."},
		"vi": {"Các yếu tố đăng nhập của bạn sắp được đặt lại", "Việc đặt lại mã PIN và khóa truy cập đang trong thời gian chờ 24 giờ. Trong lúc đó bạn vẫn có thể đăng nhập bằng các yếu tố hiện tại, việc này sẽ hủy yêu cầu đặt lại."},
	},
	"email-change-confirm": {
		"en": {"Confirm your new email address", "Open this link within 24 hours to confirm this address as the new email address of your account:"},
		"vi": {"Xác nhận địa chỉ email mới", "Hãy mở liên kết này trong vòng 24 giờ để xác nhận địa chỉ này là email mới của tài khoản:"},
	},
	"email-changed": {
		"en": {"Your email address was changed", "The email address of your account was changed. This message goes to the previous address."},
		"vi": {"Địa chỉ email đã được thay đổi", "Địa chỉ email của tài khoản đã được thay đổi. Thư này được gửi đến địa chỉ cũ."},
	},
	"password-changed": {
		"en": {"Your password was changed", "The password of your account was changed and your other sessions were signed out."},
		"vi": {"Mật khẩu đã được thay đổi", "Mật khẩu của tài khoản đã được thay đổi và các phiên đăng nhập khác đã bị đăng xuất."},
	},
	"factor-added": {
		"en": {"A sign-in factor was added", "A PIN or passkey was added to your account."},
		"vi": {"Đã thêm một yếu tố đăng nhập", "Một mã PIN hoặc khóa truy cập đã được thêm vào tài khoản của bạn."},
	},
	"factor-removed": {
		"en": {"A sign-in factor was removed", "A PIN or passkey was removed from your account."},
		"vi": {"Đã xóa một yếu tố đăng nhập", "Một mã PIN hoặc khóa truy cập đã bị xóa khỏi tài khoản của bạn."},
	},
	"recovery-code-used": {
		"en": {"A recovery code was used", "One of your recovery codes was used to sign in. Each code works once."},
		"vi": {"Một mã khôi phục đã được sử dụng", "Một trong các mã khôi phục của bạn đã được dùng để đăng nhập. Mỗi mã chỉ dùng được một lần."},
	},
	"account-locked": {
		"en": {"Your account is temporarily locked", "Too many failed sign-in attempts locked your account for a short time."},
		"vi": {"Tài khoản tạm thời bị khóa", "Quá nhiều lần đăng nhập thất bại đã khiến tài khoản của bạn bị khóa trong một thời gian ngắn."},
	},
	"account-deleted": {
		"en": {"Your account was deleted", "Your account and its data were deleted."},
		"vi": {"Tài khoản đã bị xóa", "Tài khoản và dữ liệu của bạn đã bị xóa."},
	},
}
