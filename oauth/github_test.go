package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectGitHubEmailPrefersVerifiedPrimaryAddress(t *testing.T) {
	email := selectGitHubEmail([]gitHubEmail{
		{Email: "unverified@example.com", Primary: true},
		{Email: "verified-secondary@example.com", Verified: true},
		{Email: "verified-primary@example.com", Primary: true, Verified: true},
	})

	require.Equal(t, "verified-primary@example.com", email)
	require.Empty(t, selectGitHubEmail([]gitHubEmail{{Email: "unverified@example.com", Primary: true}}))
}

func TestGitHubProviderFetchesPrivateEmailFromUserEmails(t *testing.T) {
	var authorizationHeader string
	var acceptHeader string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorizationHeader = request.Header.Get("Authorization")
		acceptHeader = request.Header.Get("Accept")
		switch request.URL.Path {
		case "/user":
			_, _ = writer.Write([]byte(`{"id":123,"login":"octocat","name":"Octo Cat","email":null}`))
		case "/user/emails":
			_, _ = writer.Write([]byte(`[{"email":"private@example.com","primary":true,"verified":true}]`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	previousBaseURL := githubAPIBaseURL
	githubAPIBaseURL = server.URL
	defer func() { githubAPIBaseURL = previousBaseURL }()

	user, err := (&GitHubProvider{}).GetUserInfo(context.Background(), &OAuthToken{AccessToken: "github-token"})
	require.NoError(t, err)
	require.Equal(t, "private@example.com", user.Email)
	require.Equal(t, "Bearer github-token", authorizationHeader)
	require.Equal(t, "application/vnd.github+json", acceptHeader)
}

func TestGitHubProviderKeepsPublicEmailWhenNoVerifiedPrivateEmailExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/user":
			_, _ = writer.Write([]byte(`{"id":123,"login":"octocat","name":"Octo Cat","email":"public@example.com"}`))
		case "/user/emails":
			_, _ = writer.Write([]byte(`[{"email":"unverified@example.com","primary":true,"verified":false}]`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	previousBaseURL := githubAPIBaseURL
	githubAPIBaseURL = server.URL
	defer func() { githubAPIBaseURL = previousBaseURL }()

	user, err := (&GitHubProvider{}).GetUserInfo(context.Background(), &OAuthToken{AccessToken: "github-token"})
	require.NoError(t, err)
	require.Equal(t, "public@example.com", user.Email)
}
