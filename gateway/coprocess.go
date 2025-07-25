package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TykTechnologies/tyk-pump/analytics"
	"github.com/TykTechnologies/tyk/apidef"
	"github.com/TykTechnologies/tyk/coprocess"
	"github.com/TykTechnologies/tyk/internal/middleware"
	"github.com/TykTechnologies/tyk/user"
	"github.com/sirupsen/logrus"
)

var (
	supportedDrivers = []apidef.MiddlewareDriver{apidef.PythonDriver, apidef.LuaDriver, apidef.GrpcDriver}
	loadedDrivers    = map[apidef.MiddlewareDriver]coprocess.Dispatcher{}
)

// CoProcessMiddleware is the basic CP middleware struct.
type CoProcessMiddleware struct {
	*BaseMiddleware

	HookType         coprocess.HookType
	HookName         string
	MiddlewareDriver apidef.MiddlewareDriver
	RawBodyOnly      bool

	successHandler *SuccessHandler
}

func (m *CoProcessMiddleware) Name() string {
	return "CoProcessMiddleware"
}

// CreateCoProcessMiddleware initializes a new CP middleware, takes hook type (pre, post, etc.), hook name ("my_hook") and driver ("python").
func CreateCoProcessMiddleware(hookName string, hookType coprocess.HookType, mwDriver apidef.MiddlewareDriver, baseMid *BaseMiddleware) func(http.Handler) http.Handler {
	dMiddleware := &CoProcessMiddleware{
		BaseMiddleware:   baseMid,
		HookType:         hookType,
		HookName:         hookName,
		MiddlewareDriver: mwDriver,
		successHandler:   &SuccessHandler{baseMid.Copy()},
	}

	return baseMid.Gw.createMiddleware(dMiddleware)
}

func DoCoprocessReload() {
	log.WithFields(logrus.Fields{
		"prefix": "coprocess",
	}).Info("Reloading middlewares")
	if dispatcher := loadedDrivers[apidef.PythonDriver]; dispatcher != nil {
		dispatcher.Reload()
	}
}

// CoProcessor represents a CoProcess during the request.
type CoProcessor struct {
	Middleware *CoProcessMiddleware
}

// BuildObject constructs a CoProcessObject from a given http.Request.
func (c *CoProcessor) BuildObject(req *http.Request, res *http.Response, spec *APISpec) (*coprocess.Object, error) {
	headers := ProtoMap(req.Header)

	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	if host != "" {
		headers["Host"] = host
	}
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	miniRequestObject := &coprocess.MiniRequestObject{
		Headers:        headers,
		SetHeaders:     map[string]string{},
		DeleteHeaders:  []string{},
		Url:            req.URL.String(),
		Params:         ProtoMap(req.URL.Query()),
		AddParams:      map[string]string{},
		ExtendedParams: ProtoMap(nil),
		DeleteParams:   []string{},
		ReturnOverrides: &coprocess.ReturnOverrides{
			ResponseCode: -1,
		},
		Method:     req.Method,
		RequestUri: req.RequestURI,
		Scheme:     scheme,
	}

	if req.Body != nil {
		defer req.Body.Close()
		var err error
		miniRequestObject.RawBody, err = ioutil.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if utf8.Valid(miniRequestObject.RawBody) && !c.Middleware.RawBodyOnly {
			miniRequestObject.Body = string(miniRequestObject.RawBody)
		}
	}

	object := &coprocess.Object{
		Request:  miniRequestObject,
		HookName: c.Middleware.HookName,
		HookType: c.Middleware.HookType,
	}

	object.Spec = make(map[string]string)

	// Append spec data:
	if c.Middleware != nil {
		configDataAsJSON := []byte("{}")
		if shouldAddConfigData(c.Middleware.Spec) {
			var err error
			configDataAsJSON, err = json.Marshal(c.Middleware.Spec.ConfigData)
			if err != nil {
				return nil, err
			}
		}

		bundleHash, err := c.Middleware.Gw.getHashedBundleName(spec.CustomMiddlewareBundle)
		if err != nil {
			return nil, err
		}

		object.Spec = map[string]string{
			"OrgID":       c.Middleware.Spec.OrgID,
			"APIID":       c.Middleware.Spec.APIID,
			"bundle_hash": bundleHash,
		}

		if shouldAddConfigData(c.Middleware.Spec) {
			object.Spec["config_data"] = string(configDataAsJSON)
		}
	}

	// Encode the session object (if not a pre-process & not a custom key check):
	if object.HookType != coprocess.HookType_Pre && object.HookType != coprocess.HookType_CustomKeyCheck {
		if session := ctxGetSession(req); session != nil {
			object.Session = ProtoSessionState(session)
			// For compatibility purposes:
			object.Metadata = object.Session.Metadata
		}
	}

	// Append response data if it's available:
	if res != nil {
		resObj := &coprocess.ResponseObject{
			Headers: make(map[string]string, len(res.Header)),
		}
		for k, v := range res.Header {
			// set univalue header
			resObj.Headers[k] = v[0]

			// set multivalue header
			currentHeader := coprocess.Header{
				Key:    k,
				Values: v,
			}
			resObj.MultivalueHeaders = append(resObj.MultivalueHeaders, &currentHeader)
		}
		resObj.StatusCode = int32(res.StatusCode)
		rawBody, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return nil, err
		}
		resObj.RawBody = rawBody
		res.Body = ioutil.NopCloser(bytes.NewReader(rawBody))
		if utf8.Valid(rawBody) && !c.Middleware.RawBodyOnly {
			resObj.Body = string(rawBody)
		}
		object.Response = resObj
	}

	return object, nil
}

// ObjectPostProcess does CoProcessObject post-processing (adding/removing headers or params, etc.).
func (c *CoProcessor) ObjectPostProcess(object *coprocess.Object, r *http.Request, origURL string, origMethod string) (err error) {
	r.ContentLength = int64(len(object.Request.RawBody))
	r.Body = ioutil.NopCloser(bytes.NewReader(object.Request.RawBody))
	nopCloseRequestBody(r)

	logger := c.Middleware.Logger()

	for _, dh := range object.Request.DeleteHeaders {
		r.Header.Del(dh)
	}
	ignoreCanonical := c.Middleware.Gw.GetConfig().IgnoreCanonicalMIMEHeaderKey
	for h, v := range object.Request.SetHeaders {
		setCustomHeader(r.Header, h, v, ignoreCanonical)
	}

	updatedValues := r.URL.Query()
	for _, k := range object.Request.DeleteParams {
		updatedValues.Del(k)
	}

	for p, v := range object.Request.AddParams {
		updatedValues.Set(p, v)
	}

	parsedURL, err := url.ParseRequestURI(object.Request.Url)
	if err != nil {
		logger.Error(err)
		return
	}

	rewriteURL := ctxGetURLRewriteTarget(r)
	if rewriteURL != nil {
		ctxSetURLRewriteTarget(r, parsedURL)
		r.URL, err = url.ParseRequestURI(origURL)
		if err != nil {
			logger.Error(err)
			return
		}
	} else {
		r.URL = parsedURL
	}

	transformMethod := ctxGetTransformRequestMethod(r)
	if transformMethod != "" {
		ctxSetTransformRequestMethod(r, object.Request.Method)
		r.Method = origMethod
	} else {
		r.Method = object.Request.Method
	}

	if !reflect.DeepEqual(r.URL.Query(), updatedValues) {
		r.URL.RawQuery = updatedValues.Encode()
	}

	return
}

// CoProcessInit creates a new CoProcessDispatcher, it will be called when Tyk starts.
func (gw *Gateway) CoProcessInit() {
	if !gw.GetConfig().CoProcessOptions.EnableCoProcess {
		log.WithFields(logrus.Fields{
			"prefix": "coprocess",
		}).Info("Rich plugins are disabled")
		return
	}

	// Load gRPC dispatcher:
	if gw.GetConfig().CoProcessOptions.CoProcessGRPCServer != "" {
		var err error
		loadedDrivers[apidef.GrpcDriver], err = gw.NewGRPCDispatcher()
		if err == nil {
			log.WithFields(logrus.Fields{
				"prefix": "coprocess",
			}).Info("gRPC dispatcher was initialized")
		} else {
			log.WithFields(logrus.Fields{
				"prefix": "coprocess",
			}).WithError(err).Error("Couldn't load gRPC dispatcher")
		}
	}

}

// EnabledForSpec checks if this middleware should be enabled for a given API.
func (m *CoProcessMiddleware) EnabledForSpec() bool {

	if !m.Gw.GetConfig().CoProcessOptions.EnableCoProcess {
		log.WithFields(logrus.Fields{
			"prefix": "coprocess",
		}).Error("Your API specifies a CP custom middleware, either Tyk wasn't build with CP support or CP is not enabled in your Tyk configuration file!")
		return false
	}

	var supported bool
	for _, driver := range supportedDrivers {
		if m.Spec.CustomMiddleware.Driver == driver {
			supported = true
		}
	}

	if !supported {
		log.WithFields(logrus.Fields{
			"prefix": "coprocess",
		}).Errorf("Unsupported driver '%s'", m.Spec.CustomMiddleware.Driver)
		return false
	}

	if d, _ := loadedDrivers[m.Spec.CustomMiddleware.Driver]; d == nil {
		log.WithFields(logrus.Fields{
			"prefix": "coprocess",
		}).Errorf("Driver '%s' isn't loaded", m.Spec.CustomMiddleware.Driver)
		return false
	}

	log.WithFields(logrus.Fields{
		"prefix": "coprocess",
	}).Debug("Enabling CP middleware.")
	m.successHandler = &SuccessHandler{m.BaseMiddleware.Copy()}
	return true
}

// ProcessRequest will run any checks on the request on the way through the system, return an error to have the chain fail
func (m *CoProcessMiddleware) ProcessRequest(w http.ResponseWriter, r *http.Request, _ interface{}) (error, int) {
	if m.HookType == coprocess.HookType_CustomKeyCheck {
		if ctxGetRequestStatus(r) == StatusOkAndIgnore {
			return nil, http.StatusOK
		}
	}

	logger := m.Logger()
	logger.Debug("CoProcess Request, HookType: ", m.HookType)
	originalURL := r.URL
	authToken, _ := m.getAuthToken(apidef.CoprocessType, r)

	extractor := getIDExtractor(m.Spec)

	var returnOverrides ReturnOverrides
	var sessionID string

	if m.HookType == coprocess.HookType_CustomKeyCheck && extractor != nil {
		sessionID, returnOverrides = extractor.ExtractAndCheck(r)
		if returnOverrides.ResponseCode != 0 {
			if returnOverrides.ResponseError == "" {
				return nil, returnOverrides.ResponseCode
			}
			err := errors.New(returnOverrides.ResponseError)
			return err, returnOverrides.ResponseCode
		}
	}

	coProcessor := CoProcessor{
		Middleware: m,
	}

	object, err := coProcessor.BuildObject(r, nil, m.Spec)
	if err != nil {
		logger.WithError(err).Error("Failed to build request object")
		return errors.New("Middleware error"), 500
	}

	var origURL string
	if rewriteUrl := ctxGetURLRewriteTarget(r); rewriteUrl != nil {
		origURL = object.Request.Url
		object.Request.Url = rewriteUrl.String()
		object.Request.RequestUri = rewriteUrl.RequestURI()
	}

	var origMethod string
	if transformMethod := ctxGetTransformRequestMethod(r); transformMethod != "" {
		origMethod = r.Method
		object.Request.Method = transformMethod
	}

	t1 := time.Now()
	returnObject, err := coProcessor.Dispatch(object)
	ms := DurationToMillisecond(time.Since(t1))

	if err != nil {
		logger.WithError(err).Error("Dispatch error")
		if m.HookType == coprocess.HookType_CustomKeyCheck {
			return errors.New("Key not authorised"), 403
		} else {
			return errors.New("Middleware error"), 500
		}
	}

	m.logger.WithField("ms", ms).Debug("gRPC request processing took")

	err = coProcessor.ObjectPostProcess(returnObject, r, origURL, origMethod)
	if err != nil {
		// Restore original URL object so that it can be used by ErrorHandler:
		r.URL = originalURL
		logger.WithError(err).Error("Failed to post-process request object")
		return errors.New("Middleware error"), 500
	}

	var token string
	if returnObject.Session != nil {
		// For compatibility purposes, inject coprocess.Object.Metadata fields:
		if returnObject.Metadata != nil {
			if returnObject.Session.Metadata == nil {
				returnObject.Session.Metadata = make(map[string]string)
			}
			for k, v := range returnObject.Metadata {
				returnObject.Session.Metadata[k] = v
			}
		}

		token = returnObject.Session.Metadata["token"]
	}

	if returnObject.Request.ReturnOverrides.ResponseError != "" {
		returnObject.Request.ReturnOverrides.ResponseBody = returnObject.Request.ReturnOverrides.ResponseError
	}

	// The CP middleware indicates this is a bad auth:
	if returnObject.Request.ReturnOverrides.ResponseCode >= http.StatusBadRequest && !returnObject.Request.ReturnOverrides.OverrideError {
		logger.WithField("key", m.Gw.obfuscateKey(token)).Info("Attempted access with invalid key")

		for h, v := range returnObject.Request.ReturnOverrides.Headers {
			w.Header().Set(h, v)
		}

		// Fire Authfailed Event
		AuthFailed(m, r, token)

		// Report in health check
		reportHealthValue(m.Spec, KeyFailure, "1")

		errorMsg := "Key not authorised"
		if returnObject.Request.ReturnOverrides.ResponseBody != "" {
			errorMsg = returnObject.Request.ReturnOverrides.ResponseBody
		}

		return errors.New(errorMsg), int(returnObject.Request.ReturnOverrides.ResponseCode)
	}

	if returnObject.Request.ReturnOverrides.ResponseCode > 0 {
		for h, v := range returnObject.Request.ReturnOverrides.Headers {
			w.Header().Set(h, v)
		}
		w.WriteHeader(int(returnObject.Request.ReturnOverrides.ResponseCode))
		w.Write([]byte(returnObject.Request.ReturnOverrides.ResponseBody))

		// Record analytics data:
		res := new(http.Response)
		res.Proto = "HTTP/1.0"
		res.ProtoMajor = 1
		res.ProtoMinor = 0
		res.StatusCode = int(returnObject.Request.ReturnOverrides.ResponseCode)
		res.Body = nopCloser{
			ReadSeeker: strings.NewReader(returnObject.Request.ReturnOverrides.ResponseBody),
		}
		res.ContentLength = int64(len(returnObject.Request.ReturnOverrides.ResponseBody))
		m.successHandler.RecordHit(r, analytics.Latency(analytics.Latency{Total: int64(ms)}), int(returnObject.Request.ReturnOverrides.ResponseCode), res, false)
		return nil, middleware.StatusRespond
	}

	// Is this a CP authentication middleware?
	if coprocessAuthEnabled(m.Spec) && m.HookType == coprocess.HookType_CustomKeyCheck {
		if extractor == nil {
			sessionID = token
		}

		// The CP middleware didn't setup a session:
		if returnObject.Session == nil || token == "" {
			authHeaderValue, _ := m.getAuthToken(m.getAuthType(), r)
			AuthFailed(m, r, authHeaderValue)
			return errors.New(http.StatusText(http.StatusForbidden)), http.StatusForbidden
		}

		returnedSession := TykSessionState(returnObject.Session)

		// If the returned object contains metadata, add them to the session:
		for k, v := range returnObject.Metadata {
			returnedSession.MetaData[k] = string(v)
		}

		returnedSession.OrgID = m.Spec.OrgID
		// set a Key ID as default
		returnedSession.KeyID = token

		if err := m.ApplyPolicies(returnedSession); err != nil {
			AuthFailed(m, r, authToken)
			return errors.New(http.StatusText(http.StatusForbidden)), http.StatusForbidden
		}

		existingSession, found := m.Gw.GlobalSessionManager.SessionDetail(m.Spec.OrgID, sessionID, false)
		if found {
			returnedSession.QuotaRenews = existingSession.QuotaRenews
			returnedSession.QuotaRemaining = existingSession.QuotaRemaining

			for api := range returnedSession.AccessRights {
				if _, found := existingSession.AccessRights[api]; found {
					if !returnedSession.AccessRights[api].Limit.IsEmpty() {
						ar := returnedSession.AccessRights[api]
						ar.Limit.QuotaRenews = existingSession.AccessRights[api].Limit.QuotaRenews
						ar.Limit.QuotaRemaining = existingSession.AccessRights[api].Limit.QuotaRemaining
						returnedSession.AccessRights[api] = ar
					}
				}
			}
		}

		// Apply it second time to fix the quota
		if err := m.ApplyPolicies(returnedSession); err != nil {
			AuthFailed(m, r, authToken)
			return errors.New(http.StatusText(http.StatusForbidden)), http.StatusForbidden
		}

		returnedSession.KeyID = sessionID

		switch m.Spec.BaseIdentityProvidedBy {
		case apidef.CustomAuth, apidef.UnsetAuth:
			ctxSetSession(r, returnedSession, true, m.Gw.GetConfig().HashKeys)
		}
	}

	return nil, http.StatusOK
}

type CustomMiddlewareResponseHook struct {
	BaseTykResponseHandler
	mw *CoProcessMiddleware
}

func (h CustomMiddlewareResponseHook) Base() *BaseTykResponseHandler {
	return &h.BaseTykResponseHandler
}

func (h *CustomMiddlewareResponseHook) Init(mwDef interface{}, spec *APISpec) error {
	mwDefinition := mwDef.(apidef.MiddlewareDefinition)

	h.mw = &CoProcessMiddleware{
		BaseMiddleware: &BaseMiddleware{
			Spec: spec,
			Gw:   h.Gw,
		},
		HookName:         mwDefinition.Name,
		HookType:         coprocess.HookType_Response,
		RawBodyOnly:      mwDefinition.RawBodyOnly,
		MiddlewareDriver: spec.CustomMiddleware.Driver,
	}
	return nil
}

// getAuthType overrides BaseMiddleware.getAuthType.
func (m *CoProcessMiddleware) getAuthType() string {
	return apidef.CoprocessType
}

func (h *CustomMiddlewareResponseHook) Name() string {
	return "CustomMiddlewareResponseHook"
}

func (h *CustomMiddlewareResponseHook) HandleError(rw http.ResponseWriter, req *http.Request) {
	handler := ErrorHandler{h.mw.BaseMiddleware.Copy()}
	handler.HandleError(rw, req, "Middleware error", http.StatusInternalServerError, true)
}

func (h *CustomMiddlewareResponseHook) HandleResponse(rw http.ResponseWriter, res *http.Response, req *http.Request, ses *user.SessionState) error {

	h.logger().WithFields(logrus.Fields{
		"prefix": "coprocess",
	}).Debugf("Response hook '%s' is called", h.mw.Name())

	coProcessor := CoProcessor{
		Middleware: h.mw,
	}

	object, err := coProcessor.BuildObject(req, res, h.mw.Spec)
	if err != nil {
		h.logger().WithError(err).Debug("Couldn't build request object")
		return errors.New("Middleware error")
	}
	object.Session = ProtoSessionState(ses)

	retObject, err := coProcessor.Dispatch(object)
	if err != nil {
		h.logger().WithError(err).Debug("Couldn't dispatch request object")
		return errors.New("Middleware error")
	}

	if retObject.Response == nil {
		h.logger().WithError(err).Debug("No response object returned by response hook")
		return errors.New("Middleware error")
	}

	// Clear all response headers before populating from coprocess response object:
	for k := range res.Header {
		delete(res.Header, k)
	}

	// check if we have changes in headers
	if !areMapsEqual(object.Response.Headers, retObject.Response.Headers) {
		// as we have changes we need to synchronize them
		retObject.Response.MultivalueHeaders = syncHeadersAndMultiValueHeaders(retObject.Response.Headers, retObject.Response.MultivalueHeaders)
	}

	// Set headers:
	ignoreCanonical := h.mw.Gw.GetConfig().IgnoreCanonicalMIMEHeaderKey
	for _, v := range retObject.Response.MultivalueHeaders {
		setCustomHeaderMultipleValues(res.Header, v.Key, v.Values, ignoreCanonical)
	}

	// Set response body:
	bodyBuf := bytes.NewBuffer(retObject.Response.RawBody)
	res.Body = ioutil.NopCloser(bodyBuf)

	//set response body length with the size of response body returned from the hook
	//so that it is updated accordingly in the response object
	responseBodyLen := len(retObject.Response.RawBody)
	res.ContentLength = int64(responseBodyLen)
	res.Header.Set("Content-Length", fmt.Sprintf("%d", responseBodyLen))

	res.StatusCode = int(retObject.Response.StatusCode)
	return nil
}

// syncHeadersAndMultiValueHeaders synchronizes the content of 'headers' and 'multiValueHeaders'.
// If a key is updated or added in 'headers', the corresponding key in 'multiValueHeaders' is also updated or added.
// If a key is removed from 'headers', the corresponding key in 'multiValueHeaders' is also removed.
// If multiValuesHeaders contains a key with multiple values and the same key is present in headers, the first value in multiValuesHeaders is updated with the value from headers, while the remaining values remain unchanged.
func syncHeadersAndMultiValueHeaders(headers map[string]string, multiValueHeaders []*coprocess.Header) []*coprocess.Header {
	updatedMultiValueHeaders := []*coprocess.Header{}

	for k, v := range headers {
		found := false
		for _, header := range multiValueHeaders {
			if header.Key == k {
				found = true

				// if the key is present in multiValueHeaders, update the first value with the value from headers
				if len(header.Values) > 0 {
					header.Values[0] = v
				}

				break
			}
		}

		if !found {
			newHeader := &coprocess.Header{
				Key:    k,
				Values: []string{v},
			}
			updatedMultiValueHeaders = append(updatedMultiValueHeaders, newHeader)
		}
	}

	// Append any existing headers that are still in the headers map
	for _, header := range multiValueHeaders {
		if _, ok := headers[header.Key]; ok {
			updatedMultiValueHeaders = append(updatedMultiValueHeaders, header)
		}
	}

	return updatedMultiValueHeaders
}

func (c *CoProcessor) Dispatch(object *coprocess.Object) (*coprocess.Object, error) {
	dispatcher := loadedDrivers[c.Middleware.MiddlewareDriver]
	if dispatcher == nil {
		err := fmt.Errorf("Couldn't dispatch request, driver '%s' isn't available", c.Middleware.MiddlewareDriver)
		return nil, err
	}
	newObject, err := dispatcher.Dispatch(object)
	if err != nil {
		return nil, err
	}
	return newObject, nil
}

func coprocessAuthEnabled(spec *APISpec) bool {
	return spec.EnableCoProcessAuth || spec.CustomPluginAuthEnabled
}

func getIDExtractor(spec *APISpec) IdExtractor {
	if !coprocessAuthEnabled(spec) {
		return nil
	}

	if spec.CustomMiddleware.IdExtractor.Disabled {
		return nil
	}

	if extractor, ok := spec.CustomMiddleware.IdExtractor.Extractor.(IdExtractor); ok {
		return extractor
	}

	return nil
}

func shouldAddConfigData(spec *APISpec) bool {
	return !spec.ConfigDataDisabled && len(spec.ConfigData) > 0
}
