package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"
)

func authFilePath() string {
	return filepath.Join(session.VibecastDir(), "auth.json")
}

// LoadAuth reads the saved auth data from disk.
func LoadAuth() (*types.AuthData, error) {
	data, err := os.ReadFile(authFilePath())
	if err != nil {
		return nil, err
	}
	var auth types.AuthData
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

// SaveAuth writes auth data to disk.
func SaveAuth(data *types.AuthData) error {
	dir := filepath.Dir(authFilePath())
	os.MkdirAll(dir, 0700)
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(authFilePath(), out, 0600)
}

// RefreshToken refreshes the OAuth token.
func RefreshToken(auth *types.AuthData) error {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", auth.KeycloakURL, auth.Realm)
	form := fmt.Sprintf("grant_type=refresh_token&client_id=vibecast-cli&refresh_token=%s", auth.RefreshToken)
	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form))
	if err != nil {
		return fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	auth.AccessToken = tokenResp.AccessToken
	auth.RefreshToken = tokenResp.RefreshToken
	auth.ExpiresAt = time.Now().Unix() + tokenResp.ExpiresIn

	if user, err := DecodeJWTUser(tokenResp.AccessToken); err == nil {
		auth.User = *user
	}

	return SaveAuth(auth)
}

// GetValidToken returns a valid access token, refreshing if needed.
func GetValidToken() (string, *types.AuthData, error) {
	auth, err := LoadAuth()
	if err != nil {
		return "", nil, err
	}

	if time.Now().Unix() > auth.ExpiresAt-30 {
		if err := RefreshToken(auth); err != nil {
			return "", nil, fmt.Errorf("token refresh failed: %w", err)
		}
	}

	return auth.AccessToken, auth, nil
}

// DecodeJWTUser extracts user info from a JWT access token.
func DecodeJWTUser(token string) (*types.AuthUserInfo, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format")
	}

	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims struct {
		Sub              string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Email            string `json:"email"`
		Picture          string `json:"picture"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	return &types.AuthUserInfo{
		Sub:              claims.Sub,
		PreferredUsername: claims.PreferredUsername,
		Email:            claims.Email,
		Picture:          claims.Picture,
	}, nil
}

// FetchAuthConfig fetches the auth configuration from the server.
func FetchAuthConfig(serverHost string) (*types.AuthConfigResponse, error) {
	scheme := "https"
	if util.IsLocalHost(serverHost) {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/api/lives/auth-config", scheme, serverHost)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("auth-config returned status %d", resp.StatusCode)
	}
	var config types.AuthConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// GeneratePKCEVerifier generates a PKCE code verifier.
func GeneratePKCEVerifier() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// GeneratePKCEChallenge generates a PKCE code challenge from a verifier.
func GeneratePKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// GenerateState generates a random state parameter.
func GenerateState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// OpenBrowser opens a URL in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch {
	case util.FileExists("/usr/bin/xdg-open"):
		cmd = exec.Command("xdg-open", url)
	default:
		cmd = exec.Command("open", url)
	}
	return cmd.Start()
}

// StartTokenRefreshLoop runs in the background and refreshes the auth token before it expires.
func StartTokenRefreshLoop(ctx context.Context) {
	auth, err := LoadAuth()
	if err != nil {
		return
	}

	for {
		sleepDuration := time.Until(time.Unix(auth.ExpiresAt-30, 0))
		if sleepDuration < 10*time.Second {
			sleepDuration = 10 * time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepDuration):
			if err := RefreshToken(auth); err != nil {
				util.DebugLog("[auth] token refresh failed: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
			} else {
				util.DebugLog("[auth] token refreshed successfully")
			}
		}
	}
}

// HandleLoginCommand implements the "vibecast login" subcommand.
func HandleLoginCommand() {
	serverHost := util.GetServerHost()
	fmt.Printf("Discovering auth configuration from %s...\n", serverHost)

	config, err := FetchAuthConfig(serverHost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not fetch auth config: %v\n", err)
		os.Exit(1)
	}

	if !config.AuthRequired {
		fmt.Println("Note: authentication is not required on this server, but you can still log in.")
	}

	if config.KeycloakURL == "" || config.Realm == "" || config.ClientID == "" {
		fmt.Fprintf(os.Stderr, "Error: server did not return valid auth configuration.\n")
		os.Exit(1)
	}

	verifier := GeneratePKCEVerifier()
	challenge := GeneratePKCEChallenge(verifier)
	state := GenerateState()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not start local server: %v\n", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	authorizeURL := fmt.Sprintf(
		"%s/realms/%s/protocol/openid-connect/auth?client_id=%s&response_type=code&redirect_uri=%s&scope=openid+profile+email&code_challenge=%s&code_challenge_method=S256&state=%s",
		config.KeycloakURL, config.Realm, config.ClientID, redirectURI, challenge, state,
	)

	fmt.Printf("Opening browser to log in... (or visit: %s)\n", authorizeURL)
	OpenBrowser(authorizeURL)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		returnedState := r.URL.Query().Get("state")
		if returnedState != state {
			http.Error(w, "Invalid state parameter", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errDesc := r.URL.Query().Get("error_description")
			if errDesc == "" {
				errDesc = r.URL.Query().Get("error")
			}
			http.Error(w, "Login failed", http.StatusBadRequest)
			errCh <- fmt.Errorf("login failed: %s", errDesc)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h2>Login successful!</h2><p>You can close this tab.</p><script>window.close()</script></body></html>`)
		codeCh <- code
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		srv.Close()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	case <-time.After(120 * time.Second):
		srv.Close()
		fmt.Fprintf(os.Stderr, "Error: login timed out (2 minutes)\n")
		os.Exit(1)
	}

	srv.Close()

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", config.KeycloakURL, config.Realm)
	form := fmt.Sprintf(
		"grant_type=authorization_code&client_id=%s&code=%s&redirect_uri=%s&code_verifier=%s",
		config.ClientID, code, redirectURI, verifier,
	)
	tokenResp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: token exchange failed: %v\n", err)
		os.Exit(1)
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != 200 {
		body, _ := io.ReadAll(tokenResp.Body)
		fmt.Fprintf(os.Stderr, "Error: token exchange failed (status %d): %s\n", tokenResp.StatusCode, string(body))
		os.Exit(1)
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to decode token response: %v\n", err)
		os.Exit(1)
	}

	user, err := DecodeJWTUser(tokenData.AccessToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not decode user info from token: %v\n", err)
		user = &types.AuthUserInfo{}
	}

	authData := &types.AuthData{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tokenData.ExpiresIn,
		KeycloakURL:  config.KeycloakURL,
		Realm:        config.Realm,
		User:         *user,
	}

	if err := SaveAuth(authData); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to save auth: %v\n", err)
		os.Exit(1)
	}

	username := user.PreferredUsername
	if username == "" {
		username = user.Email
	}
	if username == "" {
		username = user.Sub
	}
	fmt.Printf("Logged in as %s\n", username)
}

// HandleLogoutCommand implements the "vibecast logout" subcommand.
func HandleLogoutCommand() {
	path := authFilePath()
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Not logged in.")
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Println("Logged out successfully.")
}
