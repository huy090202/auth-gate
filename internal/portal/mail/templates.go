package mail

type tpl struct{ subject, body string }

// Kinds lists every email kind the Portal sends.
var Kinds = []string{
	"verify", "signup-exists", "reset", "factor-reset-confirm", "factor-reset-started",
	"email-change-confirm", "email-changed", "password-changed", "factor-added",
	"factor-removed", "recovery-code-used", "account-locked", "account-deleted",
	"org-invite", "org-removed", "org-role-changed", "org-deleted",
	"tenant-suspended", "tenant-unsuspended", "account-suspended", "account-unsuspended",
}

// linkKinds are the kinds whose whole point is a link.
var linkKinds = map[string]bool{"verify": true, "reset": true, "factor-reset-confirm": true, "email-change-confirm": true, "org-invite": true}

// orgKinds are the only kinds that take the typed vars org, inviter and role.
var orgKinds = map[string]bool{
	"org-invite": true, "org-removed": true, "org-role-changed": true, "org-deleted": true,
	"tenant-suspended": true, "tenant-unsuspended": true,
}

// operatorKinds are the notices an AuthGate administrator's action causes. They offer no
// sign-in button and no "change your password" footer: the reader did nothing wrong.
var operatorKinds = map[string]bool{
	"tenant-suspended": true, "tenant-unsuspended": true, "account-suspended": true, "account-unsuspended": true,
}

// operatorFooter closes the operator notices; the support line follows it.
var operatorFooter = map[string]string{
	"en": "This change was made by the AuthGate administrator.",
	"vi": "Thay đổi này do quản trị viên AuthGate thực hiện.",
}

// needsRole are the org kinds whose text names a role.
var needsRole = map[string]bool{"org-invite": true, "org-role-changed": true}

var roleLabel = map[string]map[string]string{
	"owner":  {"en": "owner", "vi": "chủ sở hữu"},
	"admin":  {"en": "admin", "vi": "quản trị viên"},
	"member": {"en": "member", "vi": "thành viên"},
}

// memberOf stands in for an inviter without a usable name; it has one %s for the organisation.
var memberOf = map[string]string{"en": "A member of %s", "vi": "Một thành viên của %s"}

// orgNoticeFooter closes the three organisation notices.
var orgNoticeFooter = map[string]string{
	"en": "If you think this is a mistake, ask an owner of the organisation.",
	"vi": "Nếu bạn cho rằng đây là nhầm lẫn, hãy hỏi chủ sở hữu của tổ chức.",
}

// orgFooters replace the default footer of an organisation kind.
var orgFooters = map[string]map[string]string{
	"org-removed":      orgNoticeFooter,
	"org-role-changed": orgNoticeFooter,
	"org-deleted":      orgNoticeFooter,
	"org-invite": {
		"en": "If you do not expect this invite, ignore this email; nothing changes unless you accept it.",
		"vi": "Nếu bạn không mong đợi lời mời này, hãy bỏ qua thư này; không có gì thay đổi trừ khi bạn chấp nhận.",
	},
}

var untilLabel = map[string]string{"en": "Until: ", "vi": "Cho đến: "}

// buttons are the primary button labels of the link kinds.
var buttons = map[string]map[string]string{
	"verify":               {"en": "Confirm email address", "vi": "Xác nhận email"},
	"reset":                {"en": "Choose a new password", "vi": "Chọn mật khẩu mới"},
	"factor-reset-confirm": {"en": "Confirm the reset", "vi": "Xác nhận đặt lại"},
	"email-change-confirm": {"en": "Confirm new address", "vi": "Xác nhận địa chỉ mới"},
	"org-invite":           {"en": "View the invite", "vi": "Xem lời mời"},
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
	"tenant-suspended": {
		"en": {"{org} was suspended", "The workspace {org} was suspended. Its clients stopped working and nothing in it can be changed until it is restored."},
		"vi": {"{org} đã bị tạm ngưng", "Không gian {org} đã bị tạm ngưng. Các client của nó đã ngừng hoạt động và không thể thay đổi gì trong đó cho đến khi được khôi phục."},
	},
	"tenant-unsuspended": {
		"en": {"{org} was restored", "The workspace {org} was restored. Its clients work again and it can be changed as before."},
		"vi": {"{org} đã được khôi phục", "Không gian {org} đã được khôi phục. Các client của nó hoạt động trở lại và có thể thay đổi như trước."},
	},
	"account-suspended": {
		"en": {"Your account was suspended", "Your account was suspended and you were signed out everywhere. You cannot sign in until it is restored."},
		"vi": {"Tài khoản của bạn đã bị tạm ngưng", "Tài khoản của bạn đã bị tạm ngưng và bạn đã bị đăng xuất khỏi mọi nơi. Bạn không thể đăng nhập cho đến khi tài khoản được khôi phục."},
	},
	"account-unsuspended": {
		"en": {"Your account was restored", "Your account was restored. You can sign in again."},
		"vi": {"Tài khoản của bạn đã được khôi phục", "Tài khoản của bạn đã được khôi phục. Bạn có thể đăng nhập lại."},
	},
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
	"org-invite": {
		"en": {"You're invited to {org}", "{inviter} invited you to join {org} on the AuthGate Portal as {role}. The invite expires in 7 days."},
		"vi": {"Bạn được mời vào {org}", "{inviter} đã mời bạn tham gia {org} trên AuthGate Portal với vai trò {role}. Lời mời hết hạn sau 7 ngày."},
	},
	"org-removed": {
		"en": {"You were removed from {org}", "You are no longer a member of {org}. Its clients and audit log are closed to you."},
		"vi": {"Bạn đã bị xóa khỏi {org}", "Bạn không còn là thành viên của {org}. Bạn không thể truy cập các client và nhật ký kiểm toán của tổ chức này nữa."},
	},
	"org-role-changed": {
		"en": {"Your role in {org} changed", "Your role in {org} is now {role}."},
		"vi": {"Vai trò của bạn trong {org} đã thay đổi", "Vai trò của bạn trong {org} bây giờ là {role}."},
	},
	"org-deleted": {
		"en": {"{org} was deleted", "The organisation {org} and its clients were deleted."},
		"vi": {"{org} đã bị xóa", "Tổ chức {org} và các client của tổ chức này đã bị xóa."},
	},
}

// PersonalLabel is the name a personal workspace goes by in a notice, in the reader's language.
var PersonalLabel = map[string]string{"en": "Personal workspace", "vi": "Không gian cá nhân"}
