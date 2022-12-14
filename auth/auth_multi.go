package auth

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-jwt/jwt/v4"
)

// KeycloakMultiAuthorizer is used to validate if JWT has a correct signature and is valid and returns keycloak claims
type KeycloakMultiAuthorizer struct {
	infoGetter RealmInfoGetter
}

// KeycloakRealmInfo provides keycloak realm and server information
type KeycloakRealmInfo struct {
	AuthServerUrl    string
	PEMPublicKeyCert string
	realm            string
	tokenIssuer      string
	publicKey        *rsa.PublicKey
}

func (i *KeycloakRealmInfo) validate(realm string) error {
	authUrl, err := url.ParseRequestURI(i.AuthServerUrl)
	if err != nil {
		return fmt.Errorf("couldn't parse auth server url: %w", err)
	}

	publicKey, err := jwt.ParseRSAPublicKeyFromPEM([]byte(i.PEMPublicKeyCert))
	if err != nil {
		return fmt.Errorf("couldn't parse rsa pubkey from pem cert: %w", err)
	}

	i.realm = realm
	i.publicKey = publicKey
	i.tokenIssuer = authUrl.JoinPath("/realms/" + realm).String()

	return nil
}

type RealmInfoGetter func(realm string) (KeycloakRealmInfo, error)

// NewKeycloakAuthorizerMultiRealm creates a new authorizer that checks if issuer is correct keycloak instance and realm and validates JWT signature with PEM formated public cert from keycloak.
// It also checks if Origin header mathes allowed origins from the JWT. It works for multiple realms and caches the realm information if cache is enabled,
func NewKeycloakAuthorizerMultiRealm(infoGetter RealmInfoGetter) (*KeycloakMultiAuthorizer, error) {
	if infoGetter == nil {
		return nil, errors.New("realm info getter cannot be nil")
	}

	return &KeycloakMultiAuthorizer{
		infoGetter: infoGetter,
	}, nil
}

// ParseRequest parses a request (authorization header and origin of the call), validates JWT and returns UserContext with extracted token claims
func (a KeycloakMultiAuthorizer) ParseRequest(req AuthRequest) (*UserContext, error) {
	token, err := parseAuthorizationHeader(req.AuthorizationHeader)
	if err != nil {
		return nil, fmt.Errorf("couldn't parse header: %w", err)
	}

	userCtx, err := a.ParseJWT(token)
	if err != nil {
		return nil, fmt.Errorf("couldn't parse token: %w", err)
	}

	correctOrigin := false
	for _, origin := range userCtx.allowedOrigins {
		if req.Origin == origin {
			correctOrigin = true
			break
		}
	}
	if !correctOrigin {
		return nil, fmt.Errorf("not allowed origin: %s", req.Origin)
	}

	return userCtx, nil
}

// ParseAuthorizationHeader parser an authorization header in format "BEARER JWT_TOKEN" where JWT_TOKEN is the keycloak auth token and returns UserContext with extracted token claims
func (a KeycloakMultiAuthorizer) ParseAuthorizationHeader(authHeader string) (*UserContext, error) {
	token, err := parseAuthorizationHeader(authHeader)
	if err != nil {
		return nil, fmt.Errorf("couldn't parse header: %w", err)
	}

	userCtx, err := a.ParseJWT(token)
	if err != nil {
		return nil, fmt.Errorf("couldn't parse token: %w", err)
	}

	return userCtx, nil
}

// ParseJWT parses and validated JWT token and returns UserContext with extracted token claims
func (a KeycloakMultiAuthorizer) ParseJWT(token string) (*UserContext, error) {
	jwtToken, err := jwt.ParseWithClaims(token, &customClaims{}, nil)
	if err != nil { // ValidationErrorUnverifiable
		return nil, fmt.Errorf("parsing of token failed: %w", err)
	}

	claims := jwtToken.Claims.(*customClaims)
	parts := strings.Split(claims.RegisteredClaims.Issuer, "/")
	realm := parts[len(parts)-1]

	// todo: cache
	realmInfo, err := a.infoGetter(realm)
	if err != nil {
		return nil, fmt.Errorf("couldn't get info for realm: %w", err)
	}
	if err := realmInfo.validate(realm); err != nil {
		return nil, fmt.Errorf("invalid realm info: %w", err)
	}

	_, err = jwt.Parse(token, func(*jwt.Token) (interface{}, error) {
		return realmInfo.publicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("validation of token failed: %w", err)
	}

	if claims.RegisteredClaims.Issuer != realmInfo.tokenIssuer {
		return nil, fmt.Errorf("invalid domain of issuer of token %q", claims.RegisteredClaims.Issuer)
	}

	return &UserContext{
		Realm:          realm,
		UserID:         claims.UserId,
		UserName:       claims.UserName,
		EmailAddress:   claims.Email,
		Roles:          claims.Roles,
		Groups:         claims.Groups,
		allowedOrigins: claims.AllowedOrigins,
	}, nil
}
