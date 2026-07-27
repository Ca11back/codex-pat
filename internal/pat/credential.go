package pat

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	Provider           = "codex"
	AuthKind           = "pat"
	FilePrefix         = "codex-pat-"
	ValidationValid    = "valid"
	ValidationInvalid  = "invalid"
	ValidationMismatch = "account_mismatch"

	identityHashHexLength      = 24
	maxEmailFileSegmentBytes   = 96
	maxPlanFileSegmentBytes    = 48
	maxReadableFileSuffixBytes = maxEmailFileSegmentBytes + 1 + maxPlanFileSegmentBytes
	principalHashDomain        = "codex-pat-principal-v2\x00"
)

type ownedFileDescriptor struct {
	hash string
}

var forbiddenCredentialFields = []string{
	"api_key",
	"refresh_token",
	"id_token",
	"expires_at",
	"expiry",
	"last_refresh",
}

type Credential struct {
	Type                    string            `json:"type"`
	AuthKind                string            `json:"auth_kind"`
	AccessToken             string            `json:"access_token"`
	AccountID               string            `json:"account_id"`
	ChatGPTUserID           string            `json:"chatgpt_user_id"`
	Email                   string            `json:"email,omitempty"`
	PlanType                string            `json:"plan_type"`
	ChatGPTAccountIsFedRAMP bool              `json:"chatgpt_account_is_fedramp"`
	ValidatedAt             time.Time         `json:"validated_at"`
	ValidationState         string            `json:"validation_state,omitempty"`
	Disabled                bool              `json:"disabled"`
	Headers                 map[string]string `json:"headers,omitempty"`
	BaseURL                 string            `json:"base_url,omitempty"`
	ProxyURL                string            `json:"proxy_url,omitempty"`
	Prefix                  string            `json:"prefix,omitempty"`
	Priority                json.RawMessage   `json:"priority,omitempty"`
	Note                    string            `json:"note,omitempty"`
	Websockets              bool              `json:"websockets,omitempty"`
	raw                     map[string]json.RawMessage
}

func NewCredential(token string, identity Identity, now time.Time) Credential {
	credential := Credential{
		Type:                    Provider,
		AuthKind:                AuthKind,
		AccessToken:             token,
		AccountID:               identity.AccountID,
		ChatGPTUserID:           identity.UserID,
		Email:                   identity.Email,
		PlanType:                identity.PlanType,
		ChatGPTAccountIsFedRAMP: identity.FedRAMP,
		ValidatedAt:             now.UTC(),
		ValidationState:         ValidationValid,
	}
	credential.syncFedRAMPHeader()
	return credential
}

func DecodeCredential(raw []byte) (Credential, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Credential{}, fmt.Errorf("credential JSON is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var credential Credential
	if err := decoder.Decode(&credential); err != nil {
		return Credential{}, fmt.Errorf("decode PAT credential: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Credential{}, fmt.Errorf("PAT credential contains trailing JSON")
		}
		return Credential{}, fmt.Errorf("decode trailing PAT credential data: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Credential{}, fmt.Errorf("PAT credential must be a JSON object")
	}
	credential.raw = cloneRawMap(fields)
	return credential, nil
}

// DecodeOwnedCredential identifies records owned by this plugin from their
// persisted schema. The filename is intentionally not part of primary
// ownership; callers may use strict filename matching only when JSON cannot be
// decoded and the credential must fail closed.
func DecodeOwnedCredential(raw []byte) (Credential, bool, error) {
	var marker struct {
		Type     string `json:"type"`
		AuthKind string `json:"auth_kind"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		return Credential{}, false, err
	}
	if !strings.EqualFold(strings.TrimSpace(marker.Type), Provider) || !strings.EqualFold(strings.TrimSpace(marker.AuthKind), AuthKind) {
		return Credential{}, false, nil
	}
	credential, err := DecodeCredential(raw)
	return credential, true, err
}

func (c Credential) MarshalJSON() ([]byte, error) {
	type credentialAlias Credential
	alias := credentialAlias(c)
	alias.raw = nil
	known, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	var knownFields map[string]json.RawMessage
	if err := json.Unmarshal(known, &knownFields); err != nil {
		return nil, err
	}
	fields := cloneRawMap(c.raw)
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	for _, key := range credentialFieldNames() {
		delete(fields, key)
	}
	for _, key := range forbiddenCredentialFields {
		delete(fields, key)
	}
	for key, value := range knownFields {
		fields[key] = value
	}
	if c.hasRaw("websockets") && !c.Websockets {
		fields["websockets"] = json.RawMessage("false")
	}
	return json.Marshal(fields)
}

func (c Credential) Validate() error {
	if strings.ToLower(strings.TrimSpace(c.Type)) != Provider || strings.ToLower(strings.TrimSpace(c.AuthKind)) != AuthKind {
		return fmt.Errorf("credential marker is invalid")
	}
	token, err := NormalizeToken(c.AccessToken)
	if err != nil || token != c.AccessToken {
		return fmt.Errorf("credential token is invalid")
	}
	if strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(c.ChatGPTUserID) == "" || strings.TrimSpace(c.PlanType) == "" {
		return fmt.Errorf("credential account metadata is incomplete")
	}
	if c.ValidatedAt.IsZero() {
		return fmt.Errorf("credential validation timestamp is missing")
	}
	if prefix := strings.Trim(strings.TrimSpace(c.Prefix), "/"); prefix != "" && strings.Contains(prefix, "/") {
		return fmt.Errorf("credential prefix is invalid")
	}
	if c.ChatGPTAccountIsFedRAMP {
		if len(c.Headers) != 1 || !strings.EqualFold(strings.TrimSpace(c.Headers["X-OpenAI-Fedramp"]), "true") {
			return fmt.Errorf("credential FedRAMP header is invalid")
		}
	} else if len(c.Headers) != 0 {
		return fmt.Errorf("credential contains unsupported custom headers")
	}
	if c.raw != nil {
		if _, ok := c.raw["chatgpt_account_is_fedramp"]; !ok {
			return fmt.Errorf("credential FedRAMP metadata is missing")
		}
		for _, key := range forbiddenCredentialFields {
			if value, ok := c.raw[key]; ok && len(bytes.TrimSpace(value)) > 0 && string(bytes.TrimSpace(value)) != "null" {
				return fmt.Errorf("credential contains forbidden OAuth or API-key state")
			}
		}
	}
	return nil
}

func (c *Credential) ApplyIdentity(identity Identity, now time.Time) {
	if c == nil {
		return
	}
	c.ChatGPTUserID = strings.TrimSpace(identity.UserID)
	c.Email = strings.TrimSpace(identity.Email)
	c.PlanType = strings.TrimSpace(identity.PlanType)
	c.ChatGPTAccountIsFedRAMP = identity.FedRAMP
	c.ValidatedAt = now.UTC()
	c.ValidationState = ValidationValid
	c.Disabled = false
	c.syncFedRAMPHeader()
}

func (c *Credential) Disable(state string, now time.Time) {
	if c == nil {
		return
	}
	c.Disabled = true
	c.ValidationState = strings.TrimSpace(state)
	c.ValidatedAt = now.UTC()
}

func (c *Credential) syncFedRAMPHeader() {
	if c.ChatGPTAccountIsFedRAMP {
		if c.Headers == nil {
			c.Headers = make(map[string]string)
		}
		c.Headers["X-OpenAI-Fedramp"] = "true"
		return
	}
	if c.Headers != nil {
		delete(c.Headers, "X-OpenAI-Fedramp")
		if len(c.Headers) == 0 {
			c.Headers = nil
		}
	}
}

func FileName(accountID, userID, email, planType string) string {
	name := FilePrefix + principalHash(accountID, userID)
	if emailSegment := sanitizeFileSegment(email, "@._+", maxEmailFileSegmentBytes); emailSegment != "" {
		name += "-" + emailSegment
	}
	if planSegment := sanitizeFileSegment(planType, "", maxPlanFileSegmentBytes); planSegment != "" {
		name += "-" + planSegment
	}
	return name + ".json"
}

func principalHash(accountID, userID string) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(principalHashDomain))
	var length [4]byte
	for _, value := range []string{strings.TrimSpace(accountID), strings.TrimSpace(userID)} {
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(value))
	}
	return hex.EncodeToString(hasher.Sum(nil)[:12])
}

func samePrincipal(accountID, userID, otherAccountID, otherUserID string) bool {
	accountID = strings.TrimSpace(accountID)
	userID = strings.TrimSpace(userID)
	otherAccountID = strings.TrimSpace(otherAccountID)
	otherUserID = strings.TrimSpace(otherUserID)
	return accountID != "" && userID != "" && accountID == otherAccountID && userID == otherUserID
}

func IsOwnedFile(name string) bool {
	_, ok := parseOwnedFile(name)
	return ok
}

func IsOwnedFileForPrincipal(name, accountID, userID string) bool {
	descriptor, ok := parseOwnedFile(name)
	accountID = strings.TrimSpace(accountID)
	userID = strings.TrimSpace(userID)
	if !ok || accountID == "" || userID == "" {
		return false
	}
	return descriptor.hash == principalHash(accountID, userID)
}

func parseOwnedFile(name string) (ownedFileDescriptor, bool) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return ownedFileDescriptor{}, false
	}
	name = strings.ToLower(name)
	if !strings.HasPrefix(name, FilePrefix) || !strings.HasSuffix(name, ".json") {
		return ownedFileDescriptor{}, false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, FilePrefix), ".json")
	if len(stem) < identityHashHexLength || !isLowerHex(stem[:identityHashHexLength]) {
		return ownedFileDescriptor{}, false
	}
	suffix := stem[identityHashHexLength:]
	if suffix == "" {
		return ownedFileDescriptor{hash: stem[:identityHashHexLength]}, true
	}
	if len(suffix) < 2 || suffix[0] != '-' || len(suffix)-1 > maxReadableFileSuffixBytes {
		return ownedFileDescriptor{}, false
	}
	human := suffix[1:]
	if !isASCIIAlphaNumeric(human[0]) || !isASCIIAlphaNumeric(human[len(human)-1]) {
		return ownedFileDescriptor{}, false
	}
	for i := 0; i < len(human); i++ {
		if !isASCIIAlphaNumeric(human[i]) && !strings.ContainsRune("-@._+", rune(human[i])) {
			return ownedFileDescriptor{}, false
		}
	}
	return ownedFileDescriptor{hash: stem[:identityHashHexLength]}, true
}

func sanitizeFileSegment(value, extraAllowed string, limit int) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || limit <= 0 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(min(len(value), limit))
	pendingSeparator := false
	for i := 0; i < len(value); i++ {
		character := value[i]
		allowed := isASCIIAlphaNumeric(character) || strings.ContainsRune(extraAllowed, rune(character))
		if !allowed {
			pendingSeparator = builder.Len() > 0
			continue
		}
		if pendingSeparator {
			if builder.Len()+2 > limit {
				break
			}
			builder.WriteByte('-')
			pendingSeparator = false
		}
		if builder.Len()+1 > limit {
			break
		}
		builder.WriteByte(character)
	}
	return strings.Trim(builder.String(), "-@._+")
}

func isASCIIAlphaNumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func isLowerHex(value string) bool {
	if len(value) != identityHashHexLength {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

func (c Credential) AuthData(raw []byte, fileName, path string) pluginapi.AuthData {
	_ = path
	metadata := map[string]any{
		"type":                       Provider,
		"auth_kind":                  AuthKind,
		"access_token":               c.AccessToken,
		"account_id":                 c.AccountID,
		"chatgpt_user_id":            c.ChatGPTUserID,
		"plan_type":                  c.PlanType,
		"chatgpt_account_is_fedramp": c.ChatGPTAccountIsFedRAMP,
		"validated_at":               c.ValidatedAt.UTC().Format(time.RFC3339Nano),
		"disabled":                   c.Disabled,
	}
	if c.Email != "" {
		metadata["email"] = c.Email
	}
	if c.ValidationState != "" {
		metadata["validation_state"] = c.ValidationState
	}
	if len(c.Headers) > 0 {
		metadata["headers"] = cloneStringMap(c.Headers)
	}
	if c.BaseURL != "" {
		metadata["base_url"] = c.BaseURL
	}
	if c.ProxyURL != "" {
		metadata["proxy_url"] = c.ProxyURL
	}
	if c.Prefix != "" {
		metadata["prefix"] = c.Prefix
	}
	if c.Note != "" {
		metadata["note"] = c.Note
	}
	if c.hasRaw("websockets") || c.Websockets {
		metadata["websockets"] = c.Websockets
	}

	attributes := map[string]string{"plan_type": c.PlanType}
	if c.BaseURL != "" {
		attributes["base_url"] = strings.TrimSpace(c.BaseURL)
	}
	if priority := normalizedPriority(c.Priority); priority != "" {
		attributes["priority"] = priority
	}
	if note := strings.TrimSpace(c.Note); note != "" {
		attributes["note"] = note
	}
	if c.hasRaw("websockets") || c.Websockets {
		attributes["websockets"] = strconv.FormatBool(c.Websockets)
	}
	for name, value := range c.Headers {
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name != "" && value != "" {
			attributes["header:"+name] = value
		}
	}

	return pluginapi.AuthData{
		Provider:    Provider,
		FileName:    strings.TrimSpace(fileName),
		Label:       credentialLabel(c),
		Prefix:      strings.Trim(strings.TrimSpace(c.Prefix), "/"),
		ProxyURL:    strings.TrimSpace(c.ProxyURL),
		Disabled:    c.Disabled,
		StorageJSON: append([]byte(nil), raw...),
		Metadata:    metadata,
		Attributes:  attributes,
	}
}

func disabledAuthData(raw []byte, fileName, path string) pluginapi.AuthData {
	_ = path
	return pluginapi.AuthData{
		Provider:    Provider,
		FileName:    strings.TrimSpace(fileName),
		Label:       "Codex PAT (invalid)",
		Disabled:    true,
		StorageJSON: append([]byte(nil), raw...),
		Metadata: map[string]any{
			"type":             Provider,
			"auth_kind":        AuthKind,
			"disabled":         true,
			"validation_state": "malformed",
		},
		Attributes: map[string]string{},
	}
}

func credentialLabel(c Credential) string {
	if email := strings.TrimSpace(c.Email); email != "" {
		return email
	}
	account := strings.TrimSpace(c.AccountID)
	if len(account) > 8 {
		account = account[len(account)-8:]
	}
	if account == "" {
		return "Codex PAT"
	}
	return "Codex PAT - " + account
}

func normalizedPriority(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.Itoa(number)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if value, err := strconv.Atoi(strings.TrimSpace(text)); err == nil {
			return strconv.Itoa(value)
		}
	}
	return ""
}

func (c Credential) hasRaw(key string) bool {
	_, ok := c.raw[key]
	return ok
}

func credentialFieldNames() []string {
	return []string{
		"type", "auth_kind", "access_token", "account_id", "chatgpt_user_id",
		"email", "plan_type", "chatgpt_account_is_fedramp", "validated_at",
		"validation_state", "disabled", "headers", "base_url", "proxy_url",
		"prefix", "priority", "note", "websockets",
	}
}

func cloneRawMap(src map[string]json.RawMessage) map[string]json.RawMessage {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]json.RawMessage, len(src))
	for key, value := range src {
		dst[key] = append(json.RawMessage(nil), value...)
	}
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func rewriteSecuredCredential(path, expectedName string, raw []byte) error {
	path = strings.TrimSpace(path)
	if path == "" || filepath.Base(path) != expectedName {
		return fmt.Errorf("host returned an unexpected auth file path")
	}
	if len(bytes.TrimSpace(raw)) == 0 || !json.Valid(raw) {
		return fmt.Errorf("credential payload is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect saved auth file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("saved auth file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("saved auth file must be regular")
	}
	if err := replaceFile0600(path, raw); err != nil {
		return fmt.Errorf("rewrite saved auth file securely: %w", err)
	}
	return nil
}
