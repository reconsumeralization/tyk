package gateway

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"

	"github.com/TykTechnologies/tyk/apidef"
	"github.com/TykTechnologies/tyk/config"
	"github.com/TykTechnologies/tyk/test"
	"github.com/TykTechnologies/tyk/user"

	"github.com/TykTechnologies/tyk/internal/cache"
	"github.com/TykTechnologies/tyk/internal/uuid"
)

// openssl rsa -in app.rsa -pubout > app.rsa.pub
const jwtRSAPubKey = `
-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAyqZ4rwKF8qCExS7kpY4c
nJa/37FMkJNkalZ3OuslLB0oRL8T4c94kdF4aeNzSFkSe2n99IBI6Ssl79vbfMZb
+t06L0Q94k+/P37x7+/RJZiff4y1VGjrnrnMI2iu9l4iBBRYzNmG6eblroEMMWlg
k5tysHgxB59CSNIcD9gqk1hx4n/FgOmvKsfQgWHNlPSDTRcWGWGhB2/XgNVYG2pO
lQxAPqLhBHeqGTXBbPfGF9cHzixpsPr6GtbzPwhsQ/8bPxoJ7hdfn+rzztks3d6+
HWURcyNTLRe0mjXjjee9Z6+gZ+H+fS4pnP9tqT7IgU6ePUWTpjoiPtLexgsAa/ct
jQIDAQAB
-----END PUBLIC KEY-----
`

const jwtRSAPubKeyinvalid = `
-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAyqZ4rwKF8qCExS7kpY4c
nJa/37FMkJNkalZ3OuslLB0oRL8T4c94kdF4aeNzSFkSe2n99IBI6Ssl79vbfMZb
+t06L0Q94k+/P37x7+/RJZiff4y1VGjrnrnMI2iu9l4iBBRYzNmG6eblroEMMWlg
k5tysHgxB59CSNIcD9gqk1hx4n/FgOmvKsfQgWHNlPSDTRcWGWGhB2/XgNVYG2pO
lQxAPqLhBHeqGTXBbPfGF9cHzixpsPr6GtbzPwhsQ/8bPxoJ7hdfn+rzztks3d6+
HWURcyNTLRe0mjXjjee9Z6+gZ+H+fS4pnP9tqT7IgU6ePUWTpjoiPtLexgsAa/ct
jQIDAQAB!!!!
-----END PUBLIC KEY-----
`

func createJWTSession() *user.SessionState {
	session := user.NewSessionState()
	session.Rate = 1000000.0
	session.Allowance = session.Rate
	session.LastCheck = time.Now().Unix() - 10
	session.Per = 1.0
	session.QuotaRenewalRate = 300 // 5 minutes
	session.QuotaRenews = time.Now().Unix() + 20
	session.QuotaRemaining = 1
	session.QuotaMax = -1
	session.JWTData = user.JWTData{Secret: jwtSecret}
	return session
}

func createJWTSessionWithRSA() *user.SessionState {
	session := createJWTSession()
	session.JWTData.Secret = jwtRSAPubKey
	return session
}

func createJWTSessionWithECDSA() *user.SessionState {
	session := createJWTSession()
	session.JWTData.Secret = jwtECDSAPublicKey
	return session
}

func createJWTSessionWithRSAWithPolicy(policyID string) *user.SessionState {
	session := createJWTSessionWithRSA()
	session.SetPolicies(policyID)
	return session
}

type JwtCreator func() *user.SessionState

func (ts *Test) prepareGenericJWTSession(testName string, method string, claimName string, ApiSkipKid bool) (*APISpec, string) {

	tokenKID := testKey(testName, "token")

	var jwtToken string
	var sessionFunc JwtCreator
	switch method {
	default:
		log.Warningf("Signing method '%s' is not recognised, defaulting to HMAC signature", method)
		method = HMACSign
		fallthrough
	case HMACSign:
		sessionFunc = createJWTSession

		jwtToken = createJWKTokenHMAC(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["foo"] = "bar"
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()

			if claimName != KID {
				t.Claims.(jwt.MapClaims)[claimName] = tokenKID
				t.Header[KID] = "ignore-this-id"
			} else {
				t.Header[KID] = tokenKID
			}
		})
	case RSASign:
		sessionFunc = createJWTSessionWithRSA

		jwtToken = CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["foo"] = "bar"
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()

			if claimName != KID {
				t.Claims.(jwt.MapClaims)[claimName] = tokenKID
				t.Header[KID] = "ignore-this-id"
			} else {
				t.Header[KID] = tokenKID
			}
		})
	case ECDSASign:
		sessionFunc = createJWTSessionWithECDSA

		jwtToken = CreateJWKTokenECDSA(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["foo"] = "bar"
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()

			if claimName != KID {
				t.Claims.(jwt.MapClaims)[claimName] = tokenKID
				t.Header[KID] = "ignore-this-id"
			} else {
				t.Header[KID] = tokenKID
			}
		})
	}

	spec := ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.JWTSigningMethod = method
		spec.EnableJWT = true
		spec.Proxy.ListenPath = "/"
		spec.JWTSkipKid = ApiSkipKid
		spec.DisableRateLimit = true
		spec.DisableQuota = true

		if claimName != KID {
			spec.JWTIdentityBaseField = claimName
		}
	})[0]
	err := ts.Gw.GlobalSessionManager.UpdateSession(tokenKID, sessionFunc(), 60, false)
	if err != nil {
		log.WithError(err).Error("could not update session in Session Manager.")
	}

	return spec, jwtToken

}

func TestJWTSessionHMAC(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//If we skip the check then the Id will be taken from SUB and the call will succeed
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), HMACSign, KID, false)

	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT signed with HMAC", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func BenchmarkJWTSessionHMAC(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	//If we skip the check then the Id will be taken from SUB and the call will succeed
	_, jwtToken := ts.prepareGenericJWTSession(b.Name(), HMACSign, KID, false)

	authHeaders := map[string]string{"authorization": jwtToken}
	for i := 0; i < b.N; i++ {
		ts.Run(b, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	}
}

func TestJWTHMACIdInSubClaim(t *testing.T) {

	ts := StartTest(nil)
	defer ts.Close()

	//Same as above
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), HMACSign, SUB, true)
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/HMAC/Id in SuB/Global-skip-kid/Api-skip-kid", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})

	// For backward compatibility, if the new config are not set, and the id is in the 'sub' claim while the 'kid' claim
	// in the header is not empty, then the jwt will return 403 - "Key not authorized:token invalid, key not found"
	_, jwtToken = ts.prepareGenericJWTSession(t.Name(), HMACSign, SUB, false)
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/HMAC/Id in SuB/Global-dont-skip-kid/Api-dont-skip-kid", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: `Key not authorized`,
		})
	})

	// Case where the gw always check the 'kid' claim first but if this JWTSkipCheckKidAsId is set on the api level,
	// then it'll work
	_, jwtToken = ts.prepareGenericJWTSession(t.Name(), HMACSign, SUB, true)
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/HMAC/Id in SuB/Global-dont-skip-kid/Api-skip-kid", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func TestJWTRSAIdInSubClaim(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), RSASign, SUB, true)
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA/Id in SuB/Global-skip-kid/Api-skip-kid", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})

	_, jwtToken = ts.prepareGenericJWTSession(t.Name(), RSASign, SUB, false)
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA/Id in SuB/Global-dont-skip-kid/Api-dont-skip-kid", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: `Key not authorized`,
		})
	})

	_, jwtToken = ts.prepareGenericJWTSession(t.Name(), RSASign, SUB, true)
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA/Id in SuB/Global-dont-skip-kid/Api-skip-kid", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func TestJWTSessionRSA(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, keep backward compatibility
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func BenchmarkJWTSessionRSA(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	//default values, keep backward compatibility
	_, jwtToken := ts.prepareGenericJWTSession(b.Name(), RSASign, KID, false)

	authHeaders := map[string]string{"authorization": jwtToken}
	for i := 0; i < b.N; i++ {
		ts.Run(b, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	}
}

func TestJWTSessionFailRSA_EmptyJWT(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)

	authHeaders := map[string]string{"authorization": ""}
	t.Run("Request with empty authorization header", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: 400,
		})
	})
}

func TestJWTSessionFailRSA_NoAuthHeader(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)

	authHeaders := map[string]string{}
	t.Run("Request without authorization header", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusBadRequest, BodyMatch: `Authorization field missing`,
		})
	})
}

func TestJWTSessionFailRSA_MalformedJWT(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)

	authHeaders := map[string]string{"authorization": jwtToken + "ajhdkjhsdfkjashdkajshdkajhsdkajhsd"}
	t.Run("Request with malformed JWT", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: `Key not authorized`,
		})
	})
}

func TestJWTSessionFailRSA_MalformedJWT_NOTRACK(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	spec, jwtToken := ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)
	spec.DoNotTrack = true
	authHeaders := map[string]string{"authorization": jwtToken + "ajhdkjhsdfkjashdkajshdkajhsdkajhsd"}

	t.Run("Request with malformed JWT no track", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: `Key not authorized`,
		})
	})
}

func TestJWTSessionFailRSA_WrongJWT(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)
	authHeaders := map[string]string{"authorization": "123"}

	t.Run("Request with invalid JWT", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: `Key not authorized`,
		})
	})
}

func TestJWTSessionRSABearer(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)
	authHeaders := map[string]string{"authorization": "Bearer " + jwtToken}

	t.Run("Request with valid Bearer", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func TestJWTSessionFailRSA_WrongJWT_Signature(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()
	invalidSignToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"

	//default values, same as before (keeps backward compatibility)
	ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)
	authHeaders := map[string]string{"authorization": invalidSignToken}

	t.Run("Request with invalid JWT signature", func(t *testing.T) {
		_, _ = ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: `Key not authorized: Unexpected signing method`,
		})
	})
}

func BenchmarkJWTSessionRSABearer(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	_, jwtToken := ts.prepareGenericJWTSession(b.Name(), RSASign, KID, false)
	authHeaders := map[string]string{"authorization": "Bearer " + jwtToken}

	for i := 0; i < b.N; i++ {
		ts.Run(b, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	}
}

func TestJWTSessionRSABearerInvalid(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)
	authHeaders := map[string]string{"authorization": "Bearer: " + jwtToken} // extra ":" makes the value invalid

	t.Run("Request with invalid Bearer", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: "Key not authorized",
		})
	})
}

func TestJWTSessionRSABearerInvalidTwoBears(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//default values, same as before (keeps backward compatibility)
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), RSASign, KID, false)
	authHeaders1 := map[string]string{"authorization": "Bearer bearer" + jwtToken}

	t.Run("Request with Bearer bearer", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders1, Code: http.StatusForbidden,
		})
	})

	authHeaders2 := map[string]string{"authorization": "bearer Bearer" + jwtToken}

	t.Run("Request with bearer Bearer", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders2, Code: http.StatusForbidden,
		})
	})
}

// JWTSessionRSAWithRawSourceOnWithClientID

func (ts *Test) prepareJWTSessionRSAWithRawSourceOnWithClientID(isBench bool) string {

	spec := ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.OrgID = "default"
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTClientIDBaseField = "azp"
		spec.Proxy.ListenPath = "/"
		spec.DisableRateLimit = true
		spec.DisableQuota = true
	})[0]

	policyID := ts.CreatePolicy(func(p *user.Policy) {
		p.OrgID = "default"
		p.AccessRights = map[string]user.AccessDefinition{
			spec.APIID: {
				APIName:  spec.APIDefinition.Name,
				APIID:    spec.APIID,
				Versions: []string{"default"},
			},
		}
	})

	tokenID := ""
	if isBench {
		tokenID = uuid.New()
	} else {
		tokenID = "1234567891010101"
	}
	session := createJWTSessionWithRSAWithPolicy(policyID)

	ts.Gw.GlobalSessionManager.ResetQuota(tokenID, session, false)
	err := ts.Gw.GlobalSessionManager.UpdateSession(tokenID, session, 60, false)
	if err != nil {
		log.WithError(err).Error("could not update session in Session Manager.")
	}

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = "user"
		t.Claims.(jwt.MapClaims)["azp"] = tokenID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	return jwtToken
}

func TestJWTSessionRSAWithRawSourceOnWithClientID(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	jwtToken := ts.prepareJWTSessionRSAWithRawSourceOnWithClientID(false)
	authHeaders := map[string]string{"authorization": jwtToken}

	t.Run("Initial request with no policy base field in JWT", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func BenchmarkJWTSessionRSAWithRawSourceOnWithClientID(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	jwtToken := ts.prepareJWTSessionRSAWithRawSourceOnWithClientID(true)
	authHeaders := map[string]string{"authorization": jwtToken}

	for i := 0; i < b.N; i++ {
		ts.Run(b, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	}
}

// JWTSessionRSAWithRawSource

func (ts *Test) prepareJWTSessionRSAWithRawSource() string {
	const testApiID = "test-api-id"
	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.APIID = testApiID
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
		spec.DisableRateLimit = true
		spec.DisableQuota = true
	})

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testApiID: {
				APIName: "test-api-name",
			},
		}
	})

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = "user"
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	return jwtToken
}

func TestJWTSessionRSAWithRawSource(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	jwtToken := ts.prepareJWTSessionRSAWithRawSource()

	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Initial request with valid policy", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func BenchmarkJWTSessionRSAWithRawSource(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	jwtToken := ts.prepareJWTSessionRSAWithRawSource()

	authHeaders := map[string]string{"authorization": jwtToken}

	for i := 0; i < b.N; i++ {
		ts.Run(
			b,
			test.TestCase{
				Headers: authHeaders,
				Code:    http.StatusOK,
			},
		)
	}
}

func TestJWTSessionRSAWithRawSourceInvalidPolicyID(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	spec := BuildAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})[0]

	ts.Gw.LoadAPI(spec)

	ts.CreatePolicy()

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = "user"
		t.Claims.(jwt.MapClaims)["policy_id"] = "abcxyz"
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Initial request with invalid policy", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: "key not authorized: no matching policy",
		})
	})
}

func TestJWTSessionExpiresAtValidationConfigs(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	jwtAuthHeaderGen := func(skew time.Duration) map[string]string {
		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["policy_id"] = pID
			t.Claims.(jwt.MapClaims)["user_id"] = "user123"
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(skew).Unix()
		})

		return map[string]string{"authorization": jwtToken}
	}

	spec := BuildAPI(func(spec *APISpec) {
		spec.APIID = testAPIID
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})[0]

	// This test is successful by definition
	t.Run("Expiry_After_now--Valid_jwt", func(t *testing.T) {
		t.Skip() // if you issue a 0 second skew at 0.99th of the current second? flaky test due to time math.

		spec.JWTExpiresAtValidationSkew = 0 //Default value
		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(+time.Second), Code: http.StatusOK,
		})
	})

	// This test is successful by definition, so it's true also with skew, but just to avoid confusion.
	t.Run("Expiry_After_now-Add_skew--Valid_jwt", func(t *testing.T) {
		spec.JWTExpiresAtValidationSkew = 1
		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(+time.Second), Code: http.StatusOK,
		})
	})

	t.Run("Expiry_Before_now--Invalid_jwt", func(t *testing.T) {
		spec.JWTExpiresAtValidationSkew = 0 //Default value
		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers:   jwtAuthHeaderGen(-time.Second),
			Code:      http.StatusUnauthorized,
			BodyMatch: "Key not authorized: token has expired",
		})
	})

	t.Run("Expired_token-Before_now-Huge_skew--Valid_jwt", func(t *testing.T) {
		spec.JWTExpiresAtValidationSkew = 1000 // This value doesn't matter since validation is disabled
		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(-time.Second), Code: http.StatusOK,
		})
	})

	t.Run("Expired_token-Before_now-Add_skew--Valid_jwt", func(t *testing.T) {
		spec.JWTExpiresAtValidationSkew = 2
		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(-time.Second), Code: http.StatusOK,
		})
	})
}

func TestJWTSessionIssueAtValidationConfigs(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	jwtAuthHeaderGen := func(skew time.Duration) map[string]string {
		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["policy_id"] = pID
			t.Claims.(jwt.MapClaims)["user_id"] = "user123"
			t.Claims.(jwt.MapClaims)["iat"] = time.Now().Add(skew).Unix()
		})

		return map[string]string{"authorization": jwtToken}
	}

	spec := BuildAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.JWTSigningMethod = "rsa"
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})[0]

	// This test is successful by definition
	t.Run("IssuedAt_Before_now-no_skew--Valid_jwt", func(t *testing.T) {
		spec.JWTIssuedAtValidationSkew = 0

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(-time.Second), Code: http.StatusOK,
		})
	})

	t.Run("Expiry_after_now--Invalid_jwt", func(t *testing.T) {
		spec.JWTExpiresAtValidationSkew = 0 //Default value

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(-time.Second), Code: http.StatusOK,
		})
	})

	t.Run("IssueAt-After_now-no_skew--Invalid_jwt", func(t *testing.T) {
		spec.JWTIssuedAtValidationSkew = 0

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers:   jwtAuthHeaderGen(+time.Minute),
			Code:      http.StatusUnauthorized,
			BodyMatch: "Key not authorized: token used before issued",
		})
	})

	t.Run("IssueAt--After_now-Huge_skew--valid_jwt", func(t *testing.T) {
		spec.JWTIssuedAtValidationSkew = 1000 // This value doesn't matter since validation is disabled
		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(+time.Second),
			Code:    http.StatusOK,
		})
	})

	// True by definition
	t.Run("IssueAt-Before_now-Add_skew--not_valid_jwt", func(t *testing.T) {
		spec.JWTIssuedAtValidationSkew = 2 // 2 seconds
		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(-3 * time.Second), Code: http.StatusOK,
		})
	})

	t.Run("IssueAt-After_now-Add_skew--Valid_jwt", func(t *testing.T) {
		spec.JWTIssuedAtValidationSkew = 1

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(+time.Second), Code: http.StatusOK,
		})
	})
}

func TestJWTSessionNotBeforeValidationConfigs(t *testing.T) {
	test.Flaky(t) // TODO: TT-5257 (failed on run 37/100)

	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	jwtAuthHeaderGen := func(skew time.Duration) map[string]string {
		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["policy_id"] = pID
			t.Claims.(jwt.MapClaims)["user_id"] = "user123"
			t.Claims.(jwt.MapClaims)["nbf"] = time.Now().Add(skew).Unix()
		})
		return map[string]string{"authorization": jwtToken}
	}

	spec := BuildAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.Proxy.ListenPath = "/"
		spec.JWTSigningMethod = "rsa"
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
	})[0]

	// This test is successful by definition
	t.Run("NotBefore_Before_now-Valid_jwt", func(t *testing.T) {
		spec.JWTNotBeforeValidationSkew = 0

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(-time.Second), Code: http.StatusOK,
		})
	})

	t.Run("NotBefore_After_now--Invalid_jwt", func(t *testing.T) {
		spec.JWTNotBeforeValidationSkew = 0 //Default value

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers:   jwtAuthHeaderGen(+time.Second),
			Code:      http.StatusUnauthorized,
			BodyMatch: "Key not authorized: token is not valid yet",
		})
	})

	t.Run("NotBefore_After_now-Add_skew--valid_jwt", func(t *testing.T) {
		spec.JWTNotBeforeValidationSkew = 1

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(+time.Second), Code: http.StatusOK,
		})
	})

	t.Run("NotBefore_After_now-Huge_skew--valid_jwt", func(t *testing.T) {
		spec.JWTNotBeforeValidationSkew = 1000 // This value is so high that it's actually similar to disabling the claim.

		ts.Gw.LoadAPI(spec)

		ts.Run(t, test.TestCase{
			Headers: jwtAuthHeaderGen(+time.Second), Code: http.StatusOK,
		})
	})
}

func TestJWTExistingSessionRSAWithRawSourceInvalidPolicyID(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	p1ID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	spec := BuildAPI(func(spec *APISpec) {
		spec.APIID = testAPIID
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})[0]

	ts.Gw.LoadAPI(spec)

	user_id := uuid.New()

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = user_id
		t.Claims.(jwt.MapClaims)["policy_id"] = p1ID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Initial request with valid policy", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})

	// put in JWT invalid policy ID and do request again
	jwtTokenInvalidPolicy := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = user_id
		t.Claims.(jwt.MapClaims)["policy_id"] = "abcdef"
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	authHeaders = map[string]string{"authorization": jwtTokenInvalidPolicy}
	t.Run("Request with invalid policy in JWT", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			BodyMatch: "key not authorized: no matching policy",
			Code:      http.StatusForbidden,
		})
	})
}

func TestJWTScopeToPolicyMapping(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	basePolicyID := ts.CreatePolicy(func(p *user.Policy) {
		p.ID = "base"
		p.AccessRights = map[string]user.AccessDefinition{
			"base-api": {
				Limit: user.APILimit{
					RateLimit: user.RateLimit{
						Rate: 111,
						Per:  3600,
					},
					QuotaMax: -1,
				},
			},
		}
		p.Partitions = user.PolicyPartitions{
			PerAPI: true,
		}
	})

	defaultPolicyID := ts.CreatePolicy(func(p *user.Policy) {
		p.ID = "default"
		p.AccessRights = map[string]user.AccessDefinition{
			"base-api": {
				Limit: user.APILimit{
					QuotaMax: -1,
				},
			},
		}
	})

	p1ID := ts.CreatePolicy(func(p *user.Policy) {
		p.ID = "p1"
		p.AccessRights = map[string]user.AccessDefinition{
			"api1": {
				Limit: user.APILimit{
					RateLimit: user.RateLimit{
						Rate: 100,
						Per:  60,
					},
					QuotaMax: -1,
				},
			},
		}
		p.Partitions = user.PolicyPartitions{
			PerAPI: true,
		}
	})

	p2ID := ts.CreatePolicy(func(p *user.Policy) {
		p.ID = "p2"
		p.AccessRights = map[string]user.AccessDefinition{
			"api2": {
				Limit: user.APILimit{
					RateLimit: user.RateLimit{
						Rate: 500,
						Per:  30,
					},
					QuotaMax: -1,
				},
			},
		}
		p.Partitions = user.PolicyPartitions{
			PerAPI: true,
		}
	})

	base := BuildAPI(func(spec *APISpec) {
		spec.APIID = "base-api"
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.JWTDefaultPolicies = []string{defaultPolicyID}
		spec.Proxy.ListenPath = "/base"
		spec.Scopes = apidef.Scopes{
			JWT: apidef.ScopeClaim{
				ScopeToPolicy: map[string]string{
					"user:read":  p1ID,
					"user:write": p2ID,
				},
			},
		}
		spec.OrgID = "default"
	})[0]

	spec1 := CloneAPI(base)
	spec1.APIID = "api1"
	spec1.Proxy.ListenPath = "/api1"

	spec2 := CloneAPI(base)
	spec2.APIID = "api2"
	spec2.Proxy.ListenPath = "/api2"

	spec3 := CloneAPI(base)
	spec3.APIID = "api3"
	spec3.Proxy.ListenPath = "/api3"

	ts.Gw.LoadAPI(base, spec1, spec2, spec3)

	userID := "user-" + uuid.New()
	user2ID := "user-" + uuid.New()
	user3ID := "user-" + uuid.New()

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Claims.(jwt.MapClaims)["user_id"] = userID
		t.Claims.(jwt.MapClaims)["policy_id"] = basePolicyID
		t.Claims.(jwt.MapClaims)["scope"] = "user:read user:write"
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	jwtTokenWithoutBasePol := CreateJWKToken(func(t *jwt.Token) {
		t.Claims.(jwt.MapClaims)["user_id"] = user2ID
		t.Claims.(jwt.MapClaims)["scope"] = "user:read user:write"
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	jwtTokenWithoutBasePolAndScopes := CreateJWKToken(func(t *jwt.Token) {
		t.Claims.(jwt.MapClaims)["user_id"] = user3ID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Create JWT session with base and scopes", func(t *testing.T) {
		ts.Run(t,
			test.TestCase{
				Headers: authHeaders,
				Path:    "/base",
				Code:    http.StatusOK,
			})
	})

	authHeaders = map[string]string{"authorization": jwtTokenWithoutBasePol}
	t.Run("Create JWT session without base and with scopes", func(t *testing.T) {
		ts.Run(t,
			test.TestCase{
				Headers: authHeaders,
				Path:    "/api1",
				Code:    http.StatusOK,
			})
	})

	authHeaders = map[string]string{"authorization": jwtTokenWithoutBasePolAndScopes}
	t.Run("Create JWT session without base and without scopes", func(t *testing.T) {
		ts.Run(t,
			test.TestCase{
				Headers: authHeaders,
				Path:    "/base",
				Code:    http.StatusOK,
			})
	})

	// check that key has right set of policies assigned - there should be all three - base one and two from scope
	sessionID := ts.Gw.generateToken("default", fmt.Sprintf("%x", md5.Sum([]byte(userID))))
	t.Run("Request to check that session has got both based and scope policies", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Method:    http.MethodGet,
				Path:      "/tyk/keys/" + sessionID,
				AdminAuth: true,
				Code:      http.StatusOK,
				BodyMatchFunc: func(data []byte) bool {
					sessionData := user.SessionState{}
					json.Unmarshal(data, &sessionData)

					expect := []string{basePolicyID, p1ID, p2ID}
					sort.Strings(sessionData.ApplyPolicies)
					sort.Strings(expect)

					assert.Equal(t, sessionData.ApplyPolicies, expect)
					return true
				},
			},
		)
	})

	// check that key has right set of policies assigned - there should be all three - base one and two from scope
	sessionID = ts.Gw.generateToken("default", fmt.Sprintf("%x", md5.Sum([]byte(user2ID))))
	t.Run("If scopes present no default policy should be used", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Method:    http.MethodGet,
				Path:      "/tyk/keys/" + sessionID,
				AdminAuth: true,
				Code:      http.StatusOK,
				BodyMatchFunc: func(data []byte) bool {
					sessionData := user.SessionState{}
					json.Unmarshal(data, &sessionData)
					expect := []string{p1ID, p2ID}
					sort.Strings(sessionData.ApplyPolicies)
					sort.Strings(expect)
					assert.Equal(t, sessionData.ApplyPolicies, expect)
					return true
				},
			},
		)
	})

	// check that key has right set of policies assigned - there should be all three - base one and two from scope
	sessionID = ts.Gw.generateToken("default", fmt.Sprintf("%x", md5.Sum([]byte(user3ID))))
	t.Run("Default policy should be applied if no scopes found", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Method:    http.MethodGet,
				Path:      "/tyk/keys/" + sessionID,
				AdminAuth: true,
				Code:      http.StatusOK,
				BodyMatchFunc: func(data []byte) bool {
					sessionData := user.SessionState{}
					json.Unmarshal(data, &sessionData)

					assert.Equal(t, sessionData.ApplyPolicies, []string{defaultPolicyID})

					return true
				},
			},
		)
	})

	authHeaders = map[string]string{"authorization": jwtToken}
	sessionID = ts.Gw.generateToken("default", fmt.Sprintf("%x", md5.Sum([]byte(userID))))
	// try to access api1 using JWT issued via base-api
	t.Run("Request to api1", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Headers: authHeaders,
				Method:  http.MethodGet,
				Path:    "/api1",
				Code:    http.StatusOK,
			},
		)
	})

	// try to access api2 using JWT issued via base-api
	t.Run("Request to api2", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Headers: authHeaders,
				Method:  http.MethodGet,
				Path:    "/api2",
				Code:    http.StatusOK,
			},
		)
	})

	// try to access api3 (which is not granted via base policy nor scope-policy mapping) using JWT issued via base-api
	t.Run("Request to api3", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Headers: authHeaders,
				Method:  http.MethodGet,
				Path:    "/api3",
				Code:    http.StatusForbidden,
			},
		)
	})

	// try to change scope to policy mapping and request using existing session
	p3ID := ts.CreatePolicy(func(p *user.Policy) {
		p.ID = "p3"
		p.AccessRights = map[string]user.AccessDefinition{
			spec3.APIID: {
				Limit: user.APILimit{
					RateLimit: user.RateLimit{
						Rate: 500,
						Per:  30,
					},
					QuotaMax: -1,
				},
			},
		}
		p.Partitions = user.PolicyPartitions{
			PerAPI: true,
		}
	})

	base.Scopes = apidef.Scopes{
		JWT: apidef.ScopeClaim{
			ScopeToPolicy: map[string]string{
				"user:read": p3ID,
			},
		},
	}

	ts.Gw.LoadAPI(base)

	t.Run("Request with changed scope in JWT and key with existing session", func(t *testing.T) {
		ts.Run(t,
			test.TestCase{
				Headers: authHeaders,
				Path:    "/base",
				Code:    http.StatusOK,
			})
	})

	t.Run("Request with a wrong scope in JWT and then correct scope", func(t *testing.T) {

		jwtTokenWrongScope := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["user_id"] = userID
			t.Claims.(jwt.MapClaims)["scope"] = "nonexisting"
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
		})

		authHeadersWithWrongScope := map[string]string{"authorization": jwtTokenWrongScope}

		_, _ = ts.Run(t, []test.TestCase{
			// Make consecutively to check whether caching becomes a problem
			{Path: "/base", Headers: authHeadersWithWrongScope, BodyMatch: "no matching policy found in scope claim", Code: http.StatusForbidden},
			{Path: "/base", Headers: authHeaders, Code: http.StatusOK},
			{Path: "/base", Headers: authHeadersWithWrongScope, BodyMatch: "no matching policy found in scope claim", Code: http.StatusForbidden},
		}...)
	})

	// check that key has right set of policies assigned - there should be updated list (base one and one from scope)
	t.Run("Request to check that session has got changed apply_policies value", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Method:    http.MethodGet,
				Path:      "/tyk/keys/" + sessionID,
				AdminAuth: true,
				Code:      http.StatusOK,
				BodyMatchFunc: func(data []byte) bool {
					sessionData := user.SessionState{}
					json.Unmarshal(data, &sessionData)

					assert.Equal(t, sessionData.ApplyPolicies, []string{basePolicyID, p3ID})

					return true
				},
			},
		)
	})
}

func TestGetScopeFromClaim(t *testing.T) {
	type tableTest struct {
		jwt            string
		key            string
		expectedClaims []string
		name           string
	}

	tests := []tableTest{
		{
			jwt:            `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMiwic2NvcGUiOiJmb28gYmFyIGJheiJ9.iS5FYY99ccB1oTGtMmNjM1lppS18FSKPytrV9oQouSM`,
			key:            "scope",
			expectedClaims: []string{"foo", "bar", "baz"},
			name:           "space separated",
		},
		{
			jwt:            `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMiwic2NvcGUiOlsiZm9vIiwiYmFyIiwiYmF6Il19.Lo_7J1FpUcsKWC4E9nMiouyVdUClA3KujHu9EwqHEwo`,
			key:            "scope",
			expectedClaims: []string{"foo", "bar", "baz"},
			name:           "slice strings",
		},
		{
			jwt:            `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMiwic2NvcGUxIjp7InNjb3BlMiI6ImZvbyBiYXIgYmF6In19.IsCBEl-GozS-sgZaTHoLwuBKmxYLOCYYVCiLLVmGu8o`,
			key:            "scope1.scope2",
			expectedClaims: []string{"foo", "bar", "baz"},
			name:           "nested space separated",
		},
		{
			jwt:            `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMiwic2NvcGUxIjp7InNjb3BlMiI6WyJmb28iLCJiYXIiLCJiYXoiXX19.VDBnH2U7KWl-fajAHGq6PzzWp4mnNCkfKAodfhHc0gY`,
			key:            "scope1.scope2",
			expectedClaims: []string{"foo", "bar", "baz"},
			name:           "nested slice strings",
		},
		{
			jwt:            `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMiwic2NvcGUiOlsiZm9vIGJhciIsImJheiJdfQ.XYJ5gEHQhKxLMhXrYsQ7prZ98bty9UPa7LXvF5N4IPM`,
			key:            "scope",
			expectedClaims: []string{"foo bar", "baz"},
			name:           "slice strings with spaced values",
		},
		{
			jwt:            `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMiwic2NvcGUiOlsiZm9vIGJhciIsImJheiIsWyJoZWxsbyB3b3JsZCIsIm9uZSJdXX0.A6Yc-WEZSGtOy8hBMsMrvRXNNKSDO7OLMdznoYERKWk`,
			key:            "scope",
			expectedClaims: []string{"foo bar", "baz", "hello world", "one"},
			name:           "nested slice strings with spaced values",
		},
	}

	pubKey := []byte(`mysecret`)

	for i, mytest := range tests {
		t.Run(fmt.Sprintf("%d %s", i, mytest.name), func(t *testing.T) {
			tok, err := jwt.Parse(mytest.jwt, func(token *jwt.Token) (interface{}, error) {
				return pubKey, nil
			})
			if err != nil {
				t.Fatal(err.Error())
			}

			scopes := getScopeFromClaim(tok.Claims.(jwt.MapClaims), mytest.key)
			if !testEq(mytest.expectedClaims, scopes) {
				t.Logf("expected: %v", mytest.expectedClaims)
				t.Logf("actual: %v", scopes)
				t.Fatal(i, "slices not equal")
			}
		})
	}
}

func testEq(a, b []string) bool {
	// If one is nil, the other must also be nil.
	if (a == nil) != (b == nil) {
		return false
	}

	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestJWTExistingSessionRSAWithRawSourcePolicyIDChanged(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	spec := BuildAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
		spec.OrgID = "default"
	})[0]

	ts.Gw.LoadAPI(spec)

	p1ID := ts.CreatePolicy(func(p *user.Policy) {
		p.QuotaMax = 111
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})
	p2ID := ts.CreatePolicy(func(p *user.Policy) {
		p.QuotaMax = 999
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})
	user_id := uuid.New()

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = user_id
		t.Claims.(jwt.MapClaims)["policy_id"] = p1ID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	sessionID := ts.Gw.generateToken("default", fmt.Sprintf("%x", md5.Sum([]byte(user_id))))

	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Initial request with 1st policy", func(t *testing.T) {
		ts.Run(
			t,
			test.TestCase{
				Headers: authHeaders, Code: http.StatusOK,
			},
			test.TestCase{
				Method:    http.MethodGet,
				Path:      "/tyk/keys/" + sessionID,
				AdminAuth: true,
				Code:      http.StatusOK,
				BodyMatch: `"quota_max":111`,
			},
		)
	})

	// check key/session quota

	// put in JWT another valid policy ID and do request again
	jwtTokenAnotherPolicy := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = user_id
		t.Claims.(jwt.MapClaims)["policy_id"] = p2ID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	authHeaders = map[string]string{"authorization": jwtTokenAnotherPolicy}
	t.Run("Request with new valid policy in JWT", func(t *testing.T) {
		ts.Run(t,
			test.TestCase{
				Headers: authHeaders, Code: http.StatusOK,
			},
			test.TestCase{
				Method:    http.MethodGet,
				Path:      "/tyk/keys/" + sessionID,
				AdminAuth: true,
				Code:      http.StatusOK,
				BodyMatch: `"quota_max":999`,
			},
		)
	})
}

// JWTSessionRSAWithJWK

func (ts *Test) prepareJWTSessionRSAWithJWK() string {

	const testAPIID = "test-api-id"

	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.APIID = testAPIID
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = testHttpJWK
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
		spec.DisableRateLimit = true
		spec.DisableQuota = true
	})

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = "user"
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	return jwtToken
}

func TestJWTSessionRSAWithJWK(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	jwtToken := ts.prepareJWTSessionRSAWithJWK()
	authHeaders := map[string]string{"authorization": jwtToken}

	t.Run("JWTSessionRSAWithJWK", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func BenchmarkJWTSessionRSAWithJWK(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	jwtToken := ts.prepareJWTSessionRSAWithJWK()
	authHeaders := map[string]string{"authorization": jwtToken}

	for i := 0; i < b.N; i++ {
		ts.Run(
			b,
			test.TestCase{
				Headers: authHeaders,
				Code:    http.StatusOK,
			},
		)
	}
}

// JWTSessionRSAWithEncodedJWK

func (ts *Test) prepareJWTSessionRSAWithEncodedJWK() (*APISpec, string) {

	const testAPIID = "test-api-id"
	spec := BuildAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
		spec.DisableRateLimit = true
		spec.DisableQuota = true
	})[0]

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		// Set some claims
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = "user"
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})

	return spec, jwtToken
}

func TestJWTSessionRSAWithEncodedJWK(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	spec, jwtToken := ts.prepareJWTSessionRSAWithEncodedJWK()

	authHeaders := map[string]string{"authorization": jwtToken}
	flush := func() {
		if JWKCache != nil {
			JWKCache.Flush()
		}
	}
	t.Run("Direct JWK URL", func(t *testing.T) {
		spec.JWTSource = testHttpJWK
		ts.Gw.LoadAPI(spec)
		flush()
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
	t.Run("Direct JWK URL with legacy jwk", func(t *testing.T) {
		spec.JWTSource = testHttpJWKLegacy
		ts.Gw.LoadAPI(spec)
		flush()
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
	t.Run("Base64", func(t *testing.T) {
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(testHttpJWK))
		ts.Gw.LoadAPI(spec)
		flush()
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
	t.Run("Base64 legacy jwk", func(t *testing.T) {
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(testHttpJWKLegacy))
		ts.Gw.LoadAPI(spec)
		flush()
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func TestParseRSAKeyFromJWK(t *testing.T) {
	sample := `MIIC9jCCAd6gAwIBAgIJIgAUUdWegHDtMA0GCSqGSIb3DQEBCwUAMCIxIDAeBgNVBAMTF3B1cGlsLXRlc3QuZXUuYXV0aDAuY29tMB4XDTE3MDMxMDE1MTUyMFoXDTMwMTExNzE1MTUyMFowIjEgMB4GA1UEAxMXcHVwaWwtdGVzdC5ldS5hdXRoMC5jb20wggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDWW+2PEt6nWK7cTxpkiXYTOsAWi+CCGZzDZNtwqIiLDTIkBb+Hrb70hSMRNXjPckw9+FxYC/egluGEmcEidZbj260Qp63xYpvC8XNXrlvovJqvPLk8ETPolVqYNaWM1UoJsqBPIlmFlwVH+ExCjUL37Kay3gwRXTHVRiPfPCZanqWqMu8CbC+pby1sUaiTIW1bE15v5pdgTZUH94uuMfYTdnWY6DSPWKrgwQUxmn3TJN66DynPgRjMaZaCr6FiDItm1gqE74rkbRcE3nZGM3F+fxUNTsSKjvLBBBV9aDCO408zfCycR7J+HSO2bqBxnewYhweOx23U46A0WNKW5raxAgMBAAGjLzAtMAwGA1UdEwQFMAMBAf8wHQYDVR0OBBYEFCR9T3F1LtZa3AX+LjXX9av8m/2kMA0GCSqGSIb3DQEBCwUAA4IBAQBxot91iXDzJfQVaGV+KoCDuJmOrSLTolKbJOxVoilyY72LnIcQOLgHI5JN7X17GnESTsvMC7OiUcC0RYimfrc9pchWairU/Uky6t4XmOLHQsIKjXkqwkNn3vOkRZB9wsveFQpHVLBpBUZLcPYr+8ZQYegueJpW6zSOEkswOM1U+CzERZaY6dkD8nI8TzozQ6ZLV3iypW/gx/lLT8cQb0EMzLNKSOobT+NEnhhtpy1BnfpAwV8rGENYtyUpq2FTa3kQjBCrR5cBt/07yezyeX8Amcdst3PnLaZMn5k+Elj57FKKDRV+L9rYGeceLbKKJ0uSKuhR9LIVrFaa/pzUKekC`
	b, err := base64.StdEncoding.DecodeString(sample)
	if err != nil {
		t.Fatal(err)
	}
	_, err = jwt.ParseRSAPublicKeyFromPEM(b)
	if err == nil {
		t.Error("expected an error")
	}
	_, err = ParseRSAPublicKey(b)
	assert.NoError(t, err)
}

func TestParseRSAPubKeyFromJWK(t *testing.T) {
	sample := `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAu1SU1LfVLPHCozMxH2Mo
4lgOEePzNm0tRgeLezV6ffAt0gunVTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u
+qKhbwKfBstIs+bMY2Zkp18gnTxKLxoS2tFczGkPLPgizskuemMghRniWaoLcyeh
kd3qqGElvW/VDL5AaWTg0nLVkjRo9z+40RQzuVaE8AkAFmxZzow3x+VJYKdjykkJ
0iT9wCS0DRTXu269V264Vf/3jvredZiKRkgwlL9xNAwxXFg0x/XFw005UWVRIkdg
cKWTjpBP2dPwVZ4WWC+9aGVd+Gyn1o0CLelf4rEjGoXbAAEgAqeGUxrcIlbjXfbc
mwIDAQAB
-----END PUBLIC KEY-----`

	_, err := ParseRSAPublicKey([]byte(sample))
	assert.NoError(t, err)
}

func TestAssertPS512JWT(t *testing.T) {
	signingMethod := "rsa"
	rawJWT := "eyJhbGciOiJQUzUxMiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODgiLCJuYW1lIjoiSm9obiBEb2UiLCJhZG1pbiI6dHJ1ZSwiaWF0IjoxNTE2MjM5MDIyfQ.Xm1AAFaIP12krQ57NF0FvFulIBvYPh_rtK2FgeUBN2TVbIBPBSgZ0EfdsPcGqKM1i-PeJM6PjcX_cRpdyJvMMq4xFkoEZTj6ONw4wg3kcIHBxKu8hg2qW-7voE6GGyldtQG5XmdzaayEdtuG-9mo_8BLADqbCR_-R8T3B7X1ko1TyDz0ZzMpT-46xsYPCFOMV0-u2xvqBBNfgMeXCOUzyxrl_sxw9yMgtu38qVCCRAK3lojxUjCsXMqL-wjpact0LBydX-880CU7QNAab4qdi6xA1GZhj-osJ267cHQO9Zc7G-stRMzw2zOKk3JfFQJes-t7TiMCpFdehUFNqGlgCw"
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	pubKeyPem := `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAu1SU1LfVLPHCozMxH2Mo
4lgOEePzNm0tRgeLezV6ffAt0gunVTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u
+qKhbwKfBstIs+bMY2Zkp18gnTxKLxoS2tFczGkPLPgizskuemMghRniWaoLcyeh
kd3qqGElvW/VDL5AaWTg0nLVkjRo9z+40RQzuVaE8AkAFmxZzow3x+VJYKdjykkJ
0iT9wCS0DRTXu269V264Vf/3jvredZiKRkgwlL9xNAwxXFg0x/XFw005UWVRIkdg
cKWTjpBP2dPwVZ4WWC+9aGVd+Gyn1o0CLelf4rEjGoXbAAEgAqeGUxrcIlbjXfbc
mwIDAQAB
-----END PUBLIC KEY-----`

	// Convert the PEM string to a byte slice
	pubKey := []byte(pubKeyPem)

	// Verify the token
	_, err := parser.Parse(rawJWT, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if err := assertSigningMethod(signingMethod, token); err != nil {
			return nil, err
		}

		return parseJWTKey(signingMethod, pubKey)
	})

	// Should be able to validate RS256
	assert.NoError(t, err)
}

func TestAssertNegativePS512JWT(t *testing.T) {
	signingMethod := "rsa"
	rawJWT := "eyJhbGciOiJQUzUxMiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODgiLCJuYW1lIjoiSm9obiBEb2UiLCJhZG1pbiI6dHJ1ZSwiaWF0IjoxNTE2MjM5MDIyfQ.I4IxcLO5sEMPXP_gX2UyoGN0lg2DWcRTm9w2ceSxqixE67qFODWUDNxI1TdbN4oCl9ZC_Jy8G4nJhNCu9dVptkMxnawnbIUwCsILd0SLfcAi-hFcG9K0nSzagm--6CtWlve1UbuQFW9X5fTQUESIblXbMFj6L95j4exVv1l7ch-N1Jl68fGLwoXJTQSg"
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	pubKeyPem := `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAu1SU1LfVLPHCozMxH2Mo
4lgOEePzNm0tRgeLezV6ffAt0gunVTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u
+qKhbwKfBstIs+bMY2Zkp18gnTxKLxoS2tFczGkPLPgizskuemMghRniWaoLcyeh
kd3qqGElvW/VDL5AaWTg0nLVkjRo9z+40RQzuVaE8AkAFmxZzow3x+VJYKdjykkJ
0iT9wCS0DRTXu269V264Vf/3jvredZiKRkgwlL9xNAwxXFg0x/XFw005UWVRIkdg
cKWTjpBP2dPwVZ4WWC+9aGVd+Gyn1o0CLelf4rEjGoXbAAEgAqeGUxrcIlbjXfbc
mwIDAQAB
-----END PUBLIC KEY-----`

	// Convert the PEM string to a byte slice
	pubKey := []byte(pubKeyPem)

	// Verify the token
	_, err := parser.Parse(rawJWT, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if err := assertSigningMethod(signingMethod, token); err != nil {
			return nil, err
		}

		return parseJWTKey(signingMethod, pubKey)
	})

	assert.Error(t, err)
}

func TestAssertRS256JWT(t *testing.T) {
	signingMethod := "rsa"
	rawJWT := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODgiLCJuYW1lIjoiSm9obiBEb2UiLCJhZG1pbiI6dHJ1ZSwiaWF0IjoxNTE2MjM5MDIyfQ.m2mydd79-40muPDwYbue7idTj-cKfW0jPYxcZH8-eqBc6WFJVCL--pr8IHqP-YdN7bNgfwq6iLh0kvOZ9l4Uu6xBaTdCpaXvJDfKqIqLzhltS4EfDNRkHRLDwLBvfsYt-9ijfNYvPOtTXfcIBXPby8fo529q7WYLFYR9tHAQYCLC_lS_2NieTQjAk5xAWIQ5LNItSM9iXmxhhqK47ZdzzVJnhtQ7onVY4LNgxxKqPPUQxQrq34cOBXozfA65bG7PLzvT7ais-E2_4AOXxDzspxYrDYwQFV2kjRijFcMcPc5pCWXWY9leUD1VklaSae6FuC9qJ2BATTsK8f92LSV4HA"
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	pubKeyPem := `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAu1SU1LfVLPHCozMxH2Mo
4lgOEePzNm0tRgeLezV6ffAt0gunVTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u
+qKhbwKfBstIs+bMY2Zkp18gnTxKLxoS2tFczGkPLPgizskuemMghRniWaoLcyeh
kd3qqGElvW/VDL5AaWTg0nLVkjRo9z+40RQzuVaE8AkAFmxZzow3x+VJYKdjykkJ
0iT9wCS0DRTXu269V264Vf/3jvredZiKRkgwlL9xNAwxXFg0x/XFw005UWVRIkdg
cKWTjpBP2dPwVZ4WWC+9aGVd+Gyn1o0CLelf4rEjGoXbAAEgAqeGUxrcIlbjXfbc
mwIDAQAB
-----END PUBLIC KEY-----`

	// Convert the PEM string to a byte slice
	pubKey := []byte(pubKeyPem)

	// Verify the token
	_, err := parser.Parse(rawJWT, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if err := assertSigningMethod(signingMethod, token); err != nil {
			return nil, err
		}

		return parseJWTKey(signingMethod, pubKey)
	})

	// Should be able to validate the RS256 key.
	assert.NoError(t, err)
}

func BenchmarkJWTSessionRSAWithEncodedJWK(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	spec, jwtToken := ts.prepareJWTSessionRSAWithEncodedJWK()
	spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(testHttpJWK))

	ts.Gw.LoadAPI(spec)

	authHeaders := map[string]string{"authorization": jwtToken}

	for i := 0; i < b.N; i++ {
		ts.Run(
			b,
			test.TestCase{
				Headers: authHeaders,
				Code:    http.StatusOK,
			},
		)
	}
}

func TestJWTHMACIdNewClaim(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//If we skip the check then the Id will be taken from SUB and the call will succeed
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), HMACSign, "user-id", true)
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/HMAC signature/id in user-id claim", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func TestJWTRSAIdInClaimsWithBaseField(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})
	//First test - user id in the configured base field 'user_id'
	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = "user123@test.com"
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/user id in user_id claim", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})

	//user-id claim configured but it's empty - returning an error
	jwtToken = CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = ""
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/empty user_id claim", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: "found an empty user ID in predefined base field claim user_id",
		})
	})

	//user-id claim configured but not found fallback to sub
	jwtToken = CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["sub"] = "user123@test.com"
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/user id in sub claim", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})

	//user-id claim not found fallback to sub that is empty
	jwtToken = CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["sub"] = ""
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/empty sub claim", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: "found an empty user ID in sub claim",
		})
	})

	//user-id and sub claims not found
	jwtToken = CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/no base field or sub claims", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: "no suitable claims for user ID were found",
		})
	})
}

func TestJWTRSAIdInClaimsWithoutBaseField(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = ""
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["sub"] = "user123@test.com" //is ignored
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/id found in default sub", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})

	//Id is not found since there's no sub claim and user_id has't been set in the api def (spec.JWTIdentityBaseField)
	jwtToken = CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["user_id"] = "user123@test.com" //is ignored
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders = map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/no id claims", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: "no suitable claims for user ID were found",
		})
	})
}

func TestJWTDefaultPolicies(t *testing.T) {
	const apiID = "testapid"
	const identitySource = "user_id"
	const policyFieldName = "policy_id"

	ts := StartTest(nil)
	defer ts.Close()

	defPol1 := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			apiID: {},
		}
		p.Partitions = user.PolicyPartitions{
			Quota: true,
			Acl:   true,
		}
	})

	defPol2 := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			apiID: {},
		}
		p.Partitions = user.PolicyPartitions{
			RateLimit: true,
		}
	})

	tokenPol := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			apiID: {},
		}
		p.Partitions = user.PolicyPartitions{
			Acl: true,
		}
	})

	spec := BuildAPI(func(spec *APISpec) {
		spec.APIID = apiID
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = identitySource
		spec.JWTDefaultPolicies = []string{
			defPol1,
			defPol2,
		}
		spec.Proxy.ListenPath = "/"
	})[0]

	data := []byte("dummy")
	keyID := fmt.Sprintf("%x", md5.Sum(data))
	sessionID := ts.Gw.generateToken(spec.OrgID, keyID)

	assert := func(t *testing.T, expected []string) {
		t.Helper()
		session, _ := ts.Gw.GlobalSessionManager.SessionDetail(spec.OrgID, sessionID, false)
		actual := session.PolicyIDs()
		if !reflect.DeepEqual(expected, actual) {
			t.Fatalf("Expected %v, actaul %v", expected, actual)
		}
	}

	t.Run("Policy field name empty", func(t *testing.T) {
		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)[identitySource] = "dummy"
			t.Claims.(jwt.MapClaims)[policyFieldName] = tokenPol
		})

		authHeaders := map[string]string{"authorization": jwtToken}

		// Default
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})

		// Same to check stored correctly
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})

		// Remove one of default policies
		spec.JWTDefaultPolicies = []string{defPol1}
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1})

		// Add a default policy
		spec.JWTDefaultPolicies = []string{defPol1, defPol2}
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})
	})

	t.Run("Policy field name nonempty but empty claim", func(t *testing.T) {
		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)[identitySource] = "dummy"
			t.Claims.(jwt.MapClaims)[policyFieldName] = ""
		})

		authHeaders := map[string]string{"authorization": jwtToken}

		// Default
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})

		// Same to check stored correctly
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})

		// Remove one of default policies
		spec.JWTDefaultPolicies = []string{defPol1}
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1})

		// Add a default policy
		spec.JWTDefaultPolicies = []string{defPol1, defPol2}
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})
	})

	t.Run("Policy field name nonempty invalid policy ID in claim", func(t *testing.T) {
		spec.JWTPolicyFieldName = policyFieldName
		ts.Gw.LoadAPI(spec)

		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)[identitySource] = "dummy"
			t.Claims.(jwt.MapClaims)[policyFieldName] = "invalid"
		})

		authHeaders := map[string]string{"authorization": jwtToken}

		_, _ = ts.Run(t, []test.TestCase{
			{Headers: authHeaders, Code: http.StatusForbidden},
			{Headers: authHeaders, Code: http.StatusForbidden},
		}...)

		// Reset
		spec.JWTPolicyFieldName = ""
	})

	t.Run("Default to Claim transition", func(t *testing.T) {
		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)[identitySource] = "dummy"
			t.Claims.(jwt.MapClaims)[policyFieldName] = tokenPol
		})

		authHeaders := map[string]string{"authorization": jwtToken}

		// Default
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})

		// Same to check stored correctly
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})

		// Claim
		spec.JWTPolicyFieldName = policyFieldName
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{tokenPol})
	})

	t.Run("Claim to Default transition", func(t *testing.T) {
		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)[identitySource] = "dummy"
			t.Claims.(jwt.MapClaims)[policyFieldName] = tokenPol
		})

		authHeaders := map[string]string{"authorization": jwtToken}

		// Claim
		spec.JWTPolicyFieldName = policyFieldName
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{tokenPol})

		// Same to check stored correctly
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{tokenPol})

		// Default
		spec.JWTPolicyFieldName = ""
		ts.Gw.LoadAPI(spec)
		_, _ = ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
		assert(t, []string{defPol1, defPol2})
	})
}

func TestJWTECDSASign(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//If we skip the check then the Id will be taken from SUB and the call will succeed
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), ECDSASign, KID, false)
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/ECDSA", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func TestJWTUnknownSign(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	//If we skip the check then the Id will be taken from SUB and the call will succeed
	_, jwtToken := ts.prepareGenericJWTSession(t.Name(), "bla", KID, false)
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/ECDSA signature needs a test. currently defaults to HMAC", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers: authHeaders, Code: http.StatusOK,
		})
	})
}

func TestJWTRSAInvalidPublickKey(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKeyinvalid))
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})

	pID := ts.CreatePolicy()

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Header["kid"] = "12345"
		t.Claims.(jwt.MapClaims)["foo"] = "bar"
		t.Claims.(jwt.MapClaims)["sub"] = "user123@test.com" //is ignored
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Hour * 72).Unix()
	})
	authHeaders := map[string]string{"authorization": jwtToken}
	t.Run("Request with valid JWT/RSA signature/invalid public key", func(t *testing.T) {
		ts.Run(t, test.TestCase{
			Headers:   authHeaders,
			Code:      http.StatusForbidden,
			BodyMatch: "Key not authorized",
		})
	})
}

func createExpiringPolicy(pGen ...func(p *user.Policy)) string {
	ts := StartTest(nil)
	defer ts.Close()

	pID := ts.Gw.keyGen.GenerateAuthKey("")
	pol := CreateStandardPolicy()
	pol.ID = pID
	pol.KeyExpiresIn = 1

	if len(pGen) > 0 {
		pGen[0](pol)
	}

	ts.Gw.policiesMu.Lock()
	ts.Gw.policiesByID[pID] = *pol
	ts.Gw.policiesMu.Unlock()

	return pID
}

func TestJWTExpOverride(t *testing.T) {
	test.Flaky(t) // TODO: TT-5257

	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})

	t.Run("JWT expiration bigger then policy", func(t *testing.T) {
		//create policy which sets keys to have expiry in one second
		pID := ts.CreatePolicy(func(p *user.Policy) {
			p.KeyExpiresIn = 1
			p.AccessRights = map[string]user.AccessDefinition{
				testAPIID: {
					APIName: "test-api-name",
				},
			}
		})

		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["sub"] = uuid.New()
			t.Claims.(jwt.MapClaims)["policy_id"] = pID
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Second * 72).Unix()
		})

		authHeaders := map[string]string{"authorization": jwtToken}

		//JWT expiry overrides internal token which gets expiry from policy so second request will pass
		ts.Run(t, []test.TestCase{
			{Headers: authHeaders, Code: http.StatusOK, Delay: 1100 * time.Millisecond},
			{Headers: authHeaders, Code: http.StatusOK},
		}...)
	})

	t.Run("JWT expiration smaller then policy", func(t *testing.T) {
		pID := ts.CreatePolicy(func(p *user.Policy) {
			p.KeyExpiresIn = 5
			p.AccessRights = map[string]user.AccessDefinition{
				testAPIID: {
					APIName: "test-api-name",
				},
			}
		})

		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["sub"] = uuid.New()
			t.Claims.(jwt.MapClaims)["policy_id"] = pID
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(-time.Second).Unix()
		})

		authHeaders := map[string]string{"authorization": jwtToken}

		// Should not allow expired JWTs
		ts.Run(t, []test.TestCase{
			{Headers: authHeaders, Code: http.StatusUnauthorized},
		}...)
	})

	t.Run("JWT expired but renewed, policy without expiration", func(t *testing.T) {
		pID := ts.CreatePolicy(func(p *user.Policy) {
			p.KeyExpiresIn = 0
			p.AccessRights = map[string]user.AccessDefinition{
				testAPIID: {
					APIName: "test-api-name",
				},
			}
		})

		userID := uuid.New()

		jwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["sub"] = userID
			t.Claims.(jwt.MapClaims)["policy_id"] = pID
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Second).Unix()
		})

		newJwtToken := CreateJWKToken(func(t *jwt.Token) {
			t.Claims.(jwt.MapClaims)["sub"] = userID
			t.Claims.(jwt.MapClaims)["policy_id"] = pID
			t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(5 * time.Second).Unix()
		})

		authHeaders := map[string]string{"authorization": jwtToken}
		newAuthHeaders := map[string]string{"authorization": newJwtToken}

		// Should not allow expired JWTs
		ts.Run(t, []test.TestCase{
			{Headers: authHeaders, Code: http.StatusOK, Delay: 1100 * time.Millisecond},
			{Headers: authHeaders, Code: http.StatusUnauthorized},
			{Headers: newAuthHeaders, Code: http.StatusOK},
		}...)
	})

}

func TestTimeValidateClaims(t *testing.T) {

	type testCase struct {
		name        string
		claimSkew   int64
		configSkew  uint64
		expectedErr error
	}

	t.Run("expires at", func(t *testing.T) {
		expJWTClaimsGen := func(skew int64) jwt.MapClaims {
			jsonClaims := fmt.Sprintf(`{
				"user_id": "user123",
				"exp":     %d
			}`, uint64(time.Now().Add(time.Duration(skew)*time.Second).Unix()))
			jwtClaims := jwt.MapClaims{}
			_ = json.Unmarshal([]byte(jsonClaims), &jwtClaims)
			return jwtClaims
		}

		testCases := []testCase{
			{name: "after now - valid", claimSkew: 1, configSkew: 0, expectedErr: nil},
			{name: "after now add skew - valid", claimSkew: 1, configSkew: 1, expectedErr: nil},
			{name: "before now with skew - valid", claimSkew: -1, configSkew: 1000, expectedErr: nil},
			{name: "before now - invalid", claimSkew: -1, configSkew: 1, expectedErr: jwt.ErrTokenExpired},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				jwtClaims := expJWTClaimsGen(tc.claimSkew)
				err := timeValidateJWTClaims(jwtClaims, tc.configSkew, 0, 0)
				if tc.expectedErr == nil {
					assert.Nil(t, err)
				} else {
					assert.True(t, err.Is(tc.expectedErr))
				}

			})
		}
	})

	t.Run("issued at", func(t *testing.T) {
		iatJWTClaimsGen := func(skew int64) jwt.MapClaims {
			jsonClaims := fmt.Sprintf(`{
				"user_id": "user123",
				"iat":     %d
			}`, uint64(time.Now().Add(time.Duration(skew)*time.Second).Unix()))
			jwtClaims := jwt.MapClaims{}
			_ = json.Unmarshal([]byte(jsonClaims), &jwtClaims)
			return jwtClaims
		}

		testCases := []testCase{
			{name: "before now - valid jwt", claimSkew: -1, configSkew: 0, expectedErr: nil},
			{name: "after now with large skew - valid jwt", claimSkew: 1, configSkew: 1000, expectedErr: nil},
			{name: "before now, add skew - valid jwt", claimSkew: -3, configSkew: 2, expectedErr: nil},
			{name: "after now, add skew - valid jwt", claimSkew: 1, configSkew: 1, expectedErr: nil},
			{name: "after now, no skew - invalid jwt", claimSkew: 60, configSkew: 0, expectedErr: jwt.ErrTokenUsedBeforeIssued},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				jwtClaims := iatJWTClaimsGen(tc.claimSkew)
				err := timeValidateJWTClaims(jwtClaims, 0, tc.configSkew, 0)
				if tc.expectedErr == nil {
					assert.Nil(t, err)
				} else {
					assert.True(t, err.Is(tc.expectedErr))
				}

			})
		}
	})

	t.Run("not before", func(t *testing.T) {
		nbfJWTClaimsGen := func(skew int64) jwt.MapClaims {
			jsonClaims := fmt.Sprintf(`{
				"user_id": "user123",
				"nbf":     %d
			}`, uint64(time.Now().Add(time.Duration(skew)*time.Second).Unix()))
			jwtClaims := jwt.MapClaims{}
			_ = json.Unmarshal([]byte(jsonClaims), &jwtClaims)
			return jwtClaims
		}

		testCases := []testCase{
			{name: "not before now - valid jwt", claimSkew: -1, configSkew: 0, expectedErr: nil},
			{name: "after now, add skew - valid jwt", claimSkew: 1, configSkew: 1, expectedErr: nil},
			{name: "after now with huge skew - valid_jwt", claimSkew: 1, configSkew: 1000, expectedErr: nil},
			{name: "after now - invalid jwt", claimSkew: 1, configSkew: 0, expectedErr: jwt.ErrTokenNotValidYet},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				jwtClaims := nbfJWTClaimsGen(tc.claimSkew)
				err := timeValidateJWTClaims(jwtClaims, 0, 0, tc.configSkew)
				if tc.expectedErr == nil {
					assert.Nil(t, err)
				} else {
					assert.True(t, err.Is(tc.expectedErr))
				}

			})
		}
	})
}

func TestGetUserIDFromClaim(t *testing.T) {
	userID := "123"
	userIDKey := "user_id"
	t.Run("identity base field exists", func(t *testing.T) {
		jwtClaims := jwt.MapClaims{
			userIDKey: userID,
			"iss":     "example.com",
		}
		identity, err := getUserIDFromClaim(jwtClaims, "user_id")
		assert.NoError(t, err)
		assert.Equal(t, identity, userID)
	})

	t.Run("identity base field doesn't exist, fallback to sub", func(t *testing.T) {
		jwtClaims := jwt.MapClaims{
			"iss": "example.com",
			"sub": userID,
		}
		identity, err := getUserIDFromClaim(jwtClaims, userIDKey)
		assert.NoError(t, err)
		assert.Equal(t, identity, userID)
	})

	t.Run("identity base field and sub doesn't exist", func(t *testing.T) {
		jwtClaims := jwt.MapClaims{
			"iss": "example.com",
		}
		_, err := getUserIDFromClaim(jwtClaims, userIDKey)
		assert.ErrorIs(t, err, ErrNoSuitableUserIDClaimFound)
	})

	t.Run("identity base field doesn't exist, empty sub", func(t *testing.T) {
		jwtClaims := jwt.MapClaims{
			"iss": "example.com",
			"sub": "",
		}
		_, err := getUserIDFromClaim(jwtClaims, userIDKey)
		assert.ErrorIs(t, err, ErrEmptyUserIDInSubClaim)
	})

	t.Run("empty identity base field", func(t *testing.T) {
		jwtClaims := jwt.MapClaims{
			"iss":     "example.com",
			userIDKey: "",
		}
		_, err := getUserIDFromClaim(jwtClaims, userIDKey)
		assert.Equal(t, fmt.Sprintf("found an empty user ID in predefined base field claim %s", userIDKey), err.Error())
	})
}

func TestJWTMiddleware_getSecretToVerifySignature_JWKNoKID(t *testing.T) {
	const jwkURL = "https://jwk.com"

	m := JWTMiddleware{BaseMiddleware: &BaseMiddleware{}}
	api := &apidef.APIDefinition{JWTSource: jwkURL}
	api.JWTJwksURIs = []apidef.JWK{}
	m.Spec = &APISpec{APIDefinition: api}

	token := &jwt.Token{Header: make(map[string]interface{})}
	_, err := m.getSecretToVerifySignature(nil, token)
	assert.ErrorIs(t, err, ErrKIDNotAString)

	t.Run("base64 encoded JWK URL", func(t *testing.T) {
		api.JWTSource = base64.StdEncoding.EncodeToString([]byte(api.JWTSource))
		_, err := m.getSecretToVerifySignature(nil, token)
		assert.ErrorIs(t, err, ErrKIDNotAString)
	})

	t.Run("multiple JWK URIs", func(t *testing.T) {
		api.JWTJwksURIs = []apidef.JWK{
			{
				URL: "http://localhost:8080/realms/jwt/protocol/openid-connect/certs",
			},
		}
		_, err := m.getSecretToVerifySignature(nil, token)
		assert.Error(t, err)
	})

	t.Run("multiple JWK URIs with a source", func(t *testing.T) {
		api.JWTJwksURIs = []apidef.JWK{
			{
				URL: "http://localhost:8080/realms/jwt/protocol/openid-connect/certs",
			},
		}

		api.JWTSource = jwkURL
		_, err := m.getSecretToVerifySignature(nil, token)
		assert.Error(t, err)
	})
}

func TestGetSecretFromMultipleJWKURIs(t *testing.T) {
	originalGetJWK := GetJWK
	defer func() { GetJWK = originalGetJWK }()

	const testAPIID = "test-api"
	const testJWKURL = "http://localhost:8080/realms/jwt/protocol/openid-connect/certs"
	const encodedTestJWKURL = "aHR0cDovL2xvY2FsaG9zdDo4MDgwL3JlYWxtcy9qd3QvcHJvdG9jb2wvb3BlbmlkLWNvbm5lY3QvY2VydHM="

	gw := &Gateway{}
	gw.SetConfig(config.Config{
		JWTSSLInsecureSkipVerify: true,
	})

	m := JWTMiddleware{
		BaseMiddleware: &BaseMiddleware{
			Gw: gw,
		},
	}

	api := &apidef.APIDefinition{
		APIID: "random-api",
		OrgID: "org-id",
	}

	cacheKey := JWKsAPIDef + api.APIID + api.OrgID

	api.JWTJwksURIs = []apidef.JWK{
		{
			URL: testJWKURL,
		},
	}

	m.Spec = &APISpec{
		APIDefinition: api,
	}

	createMiddleware := func(_ []apidef.JWK) *JWTMiddleware {
		return &m
	}

	tests := []struct {
		name                string
		setup               func(isOas bool)
		jwkURIs             []apidef.JWK
		jwkURI              apidef.JWK
		kid                 interface{}
		expectKey           interface{}
		expectError         error
		useGetSecretFromURL bool
		isOas               bool
	}{
		{
			name: "success with valid JWK URL and matching KID",
			setup: func(isOas bool) {
				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return &jose.JSONWebKeySet{
						Keys: []jose.JSONWebKey{
							{KeyID: "test-kid", Key: "secret-key"},
						},
					}, nil
				}

				api.IsOAS = isOas
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         "test-kid",
			expectKey:   "secret-key",
			expectError: nil,
			isOas:       true,
		},
		{
			name: "error when KID is not a string",
			setup: func(isOas bool) {
				api.IsOAS = isOas
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         12345,
			expectKey:   nil,
			expectError: ErrKIDNotAString,
			isOas:       true,
		},
		{
			name: "cache hit with unchanged URLs",
			setup: func(isOas bool) {
				api.IsOAS = isOas

				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return &jose.JSONWebKeySet{
						Keys: []jose.JSONWebKey{
							{KeyID: "cached-kid", Key: "cached-key"},
						},
					}, nil
				}

				JWKCache.Set(cacheKey, &apidef.APIDefinition{
					APIID:       testAPIID,
					JWTJwksURIs: []apidef.JWK{{URL: testJWKURL}},
				}, cache.DefaultExpiration)

				JWKCache.Set(testAPIID, &jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{
						{KeyID: "cached-kid", Key: "cached-key"},
					},
				}, cache.DefaultExpiration)
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         "cached-kid",
			expectKey:   "cached-key",
			expectError: nil,
			isOas:       true,
		},
		{
			name: "invalid JWK cache format triggers refetch",
			setup: func(isOas bool) {
				api.IsOAS = isOas

				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return &jose.JSONWebKeySet{
						Keys: []jose.JSONWebKey{
							{KeyID: "fresh-kid", Key: "fresh-key"},
						},
					}, nil
				}
				JWKCache.Set(testAPIID, "invalid-format", cache.DefaultExpiration)
				JWKCache.Set(cacheKey, &apidef.APIDefinition{
					APIID:       testAPIID,
					JWTJwksURIs: []apidef.JWK{{URL: testJWKURL}},
				}, cache.DefaultExpiration)
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         "fresh-kid",
			expectKey:   "fresh-key",
			expectError: nil,
			isOas:       true,
		},
		{
			name: "JWK URLs changed triggers refetch",
			setup: func(isOas bool) {
				api.IsOAS = isOas

				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return &jose.JSONWebKeySet{
						Keys: []jose.JSONWebKey{
							{KeyID: "new-kid", Key: "new-key"},
						},
					}, nil
				}

				JWKCache.Set(cacheKey, &apidef.APIDefinition{
					APIID: testAPIID,
					JWTJwksURIs: []apidef.JWK{
						{URL: "http://localhost:8080/old-url"},
					},
				}, cache.DefaultExpiration)

				JWKCache.Set(testAPIID, &jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{
						{KeyID: "old-kid", Key: "old-key"},
					},
				}, cache.DefaultExpiration)
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         "new-kid",
			expectKey:   "new-key",
			expectError: nil,
			isOas:       true,
		},
		{
			name: "error fetching jwks",
			setup: func(isOas bool) {
				api.IsOAS = isOas

				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return nil, errors.New("failed to fetch JWK")
				}
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         "any-kid",
			expectKey:   nil,
			expectError: errors.New("no matching KID found in any JWKs or fallback"),
			isOas:       true,
		},
		{
			name: "Cached API definition is different from expected",
			setup: func(isOas bool) {
				api.IsOAS = isOas

				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return nil, errors.New("failed to fetch JWK")
				}

				JWKCache.Set(cacheKey, &apidef.APIDefinition{
					APIID: testAPIID,
					JWTJwksURIs: []apidef.JWK{
						{URL: testJWKURL},
					},
				}, cache.DefaultExpiration)

				JWKCache.Set(api.APIID, map[string]string{"jwk": "something-random"}, cache.DefaultExpiration)
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         "new-kid",
			expectKey:   nil,
			expectError: errors.New("no matching KID found in any JWKs or fallback"),
			isOas:       true,
		},
		{
			name: "Test getSecretFromURL using getSecretFromMultipleJWKURIs data",
			setup: func(isOas bool) {
				api.IsOAS = isOas

				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return nil, errors.New("failed to fetch JWK")
				}

				JWKCache.Set(cacheKey, &apidef.APIDefinition{
					APIID:     testAPIID,
					JWTSource: encodedTestJWKURL,
				}, cache.DefaultExpiration)

				JWKCache.Set(api.APIID, map[string]string{"jwk": "something-random"}, cache.DefaultExpiration)
			},
			jwkURI:              apidef.JWK{URL: testJWKURL},
			kid:                 "new-kid",
			expectKey:           nil,
			expectError:         errors.New("failed to fetch JWK"),
			useGetSecretFromURL: true,
			isOas:               true,
		},
		{
			name: "ensure jwksURIs faeature works only with OAS",
			setup: func(isOas bool) {
				api.IsOAS = isOas

				GetJWK = func(_ string, _ bool) (*jose.JSONWebKeySet, error) {
					return &jose.JSONWebKeySet{
						Keys: []jose.JSONWebKey{
							{KeyID: "fresh-kid", Key: "fresh-key"},
						},
					}, nil
				}
				JWKCache.Set(testAPIID, "invalid-format", cache.DefaultExpiration)
				JWKCache.Set(cacheKey, &apidef.APIDefinition{
					APIID:       testAPIID,
					JWTJwksURIs: []apidef.JWK{{URL: testJWKURL}},
				}, cache.DefaultExpiration)
			},
			jwkURIs:     []apidef.JWK{{URL: testJWKURL}},
			kid:         "fresh-kid",
			expectKey:   nil,
			expectError: errors.New("this feature is only available when using OAS API"),
			isOas:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			JWKCache.Flush()

			if tt.setup != nil {
				tt.setup(tt.isOas)
			}

			mw := createMiddleware(tt.jwkURIs)

			var key interface{}
			var err error

			if !tt.useGetSecretFromURL {
				key, err = mw.getSecretFromMultipleJWKURIs(tt.jwkURIs, tt.kid, "RS256")
			} else {
				key, err = mw.getSecretFromURL(tt.jwkURI.URL, tt.kid, "RS256")
			}

			if tt.expectError != nil {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError.Error())
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectKey, key)
		})
	}
}

func TestJWT_ExtractOAuthClientIDForDCR(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	const testAPIID = "test-api-id"

	api := ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = false
		spec.APIID = testAPIID
		spec.EnableJWT = true
		spec.JWTSigningMethod = RSASign
		spec.JWTSource = base64.StdEncoding.EncodeToString([]byte(jwtRSAPubKey))
		spec.JWTIdentityBaseField = "user_id"
		spec.JWTPolicyFieldName = "policy_id"
		spec.Proxy.ListenPath = "/"
	})[0]

	pID := ts.CreatePolicy(func(p *user.Policy) {
		p.AccessRights = map[string]user.AccessDefinition{
			testAPIID: {
				APIName: "test-api-name",
			},
		}
	})

	userID := uuid.New()
	const myOKTAClientID = "myOKTAClientID"

	jwtToken := CreateJWKToken(func(t *jwt.Token) {
		t.Claims.(jwt.MapClaims)["sub"] = userID
		t.Claims.(jwt.MapClaims)["policy_id"] = pID
		t.Claims.(jwt.MapClaims)["cid"] = myOKTAClientID // cid is specific to OKTA
		t.Claims.(jwt.MapClaims)["exp"] = time.Now().Add(time.Second * 72).Unix()
	})

	authHeaders := map[string]string{"authorization": jwtToken}

	keyID := fmt.Sprintf("%x", md5.Sum([]byte(userID)))
	sessionID := ts.Gw.generateToken("default", keyID)

	t.Run("DCR enabled", func(t *testing.T) {
		_, _ = ts.Run(t, test.TestCase{Headers: authHeaders, Code: http.StatusOK})

		privateSession, found := ts.Gw.GlobalSessionManager.SessionDetail("default", sessionID, false)
		assert.True(t, found)
		assert.Equal(t, myOKTAClientID, privateSession.OauthClientID)
	})

	t.Run("DCR disabled", func(t *testing.T) {
		api.IDPClientIDMappingDisabled = true
		ts.Gw.LoadAPI(api)
		_, _ = ts.Run(t, test.TestCase{Headers: authHeaders, Code: http.StatusOK})

		privateSession, found := ts.Gw.GlobalSessionManager.SessionDetail("default", sessionID, false)
		assert.True(t, found)
		assert.Empty(t, privateSession.OauthClientID)
	})
}

func Test_getOAuthClientIDFromClaim(t *testing.T) {
	testCases := []struct {
		name             string
		claims           jwt.MapClaims
		expectedClientID string
	}{
		{
			name: "unknown",
			claims: jwt.MapClaims{
				"unknown": "value",
			},
			expectedClientID: "",
		},
		{
			name: "clientId",
			claims: jwt.MapClaims{
				"clientId": "value1",
			},
			expectedClientID: "value1",
		},
		{
			name: "cid",
			claims: jwt.MapClaims{
				"cid": "value2",
			},
			expectedClientID: "value2",
		},
		{
			name: "client_id",
			claims: jwt.MapClaims{
				"client_id": "value3",
			},
			expectedClientID: "value3",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			j := JWTMiddleware{BaseMiddleware: &BaseMiddleware{}}
			j.Spec = &APISpec{APIDefinition: &apidef.APIDefinition{}}

			oauthClientID := j.getOAuthClientIDFromClaim(tc.claims)

			assert.Equal(t, tc.expectedClientID, oauthClientID)
		})
	}
}
