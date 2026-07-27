package management

const (
	managementImportPath     = "/v0/management/plugins/codex-pat/import"
	managementStatusPath     = "/v0/management/plugins/codex-pat/status"
	managementRevalidatePath = "/v0/management/plugins/codex-pat/revalidate"
	resourceManagePath       = "/v0/resource/plugins/codex-pat/manage"
	resourceCSSPath          = "/v0/resource/plugins/codex-pat/assets/app.css"
	resourceJSPath           = "/v0/resource/plugins/codex-pat/assets/app.js"
	resourceRefreshIconPath  = "/v0/resource/plugins/codex-pat/assets/icons/refresh-cw.svg"
	resourceTrashIconPath    = "/v0/resource/plugins/codex-pat/assets/icons/trash-2.svg"
	resourceKeyIconPath      = "/v0/resource/plugins/codex-pat/assets/icons/key-round.svg"
)

type importRequest struct {
	PAT string `json:"pat"`
}

type revalidateRequest struct {
	AuthIndex string `json:"auth_index"`
}

type accountStatus struct {
	AuthIndex   string `json:"auth_index"`
	Name        string `json:"name"`
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	PlanType    string `json:"plan_type,omitempty"`
	ValidatedAt string `json:"validated_at,omitempty"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
	Readiness   string `json:"readiness"`
}

type accountList struct {
	Accounts []accountStatus `json:"accounts"`
}

type dataEnvelope struct {
	Data any `json:"data"`
}

type errorEnvelope struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
