package models

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/sessions"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// CloudProvider - represents a local provider
type CloudProvider struct {
	*BitCaskSessionPersister

	SaaSTokenName string
	SaaSBaseURL   string

	SessionName   string
	RefCookieName string

	SessionStore        sessions.Store
	loginCookieDuration time.Duration // loginCookieDuration = 1 * time.Hour
}

// GetProviderType - Returns ProviderType
func (l *CloudProvider) GetProviderType() ProviderType {
	return CloudProviderType
}

// InitiateLogin - initiates login flow and returns a true to indicate the handler to "return" or false to continue
func (l *CloudProvider) InitiateLogin(w http.ResponseWriter, r *http.Request) {
	tu := "http://" + r.Host + r.RequestURI
	token := r.URL.Query().Get(l.SaaSTokenName)
	if token == "" {
		http.SetCookie(w, &http.Cookie{
			Name:     l.RefCookieName,
			Value:    "/",
			Expires:  time.Now().Add(l.loginCookieDuration),
			Path:     "/",
			HttpOnly: true,
		})
		http.Redirect(w, r, l.SaaSBaseURL+"?source="+base64.URLEncoding.EncodeToString([]byte(tu)), http.StatusFound)
		return
	}
	l.issueSession(w, r)
	return
}

// issueSession issues a cookie session after successful login
func (l *CloudProvider) issueSession(w http.ResponseWriter, req *http.Request) {
	var reffURL string
	reffCk, _ := req.Cookie(l.RefCookieName)
	if reffCk != nil {
		reffURL = reffCk.Value
	}
	logrus.Infof("preparing to issue session. retrieved reff url: %s", reffURL)
	if reffURL == "" {
		reffURL = "/"
	}
	// session, err := h.config.SessionStore.New(req, h.config.SessionName)
	session, _ := l.SessionStore.New(req, l.SessionName)
	// if err != nil {
	// 	logrus.Errorf("unable to create session: %v", err)
	// 	http.Error(w, "unable to create session", http.StatusInternalServerError)
	// 	return
	// }
	session.Options.Path = "/"
	token := ""
	for k, va := range req.URL.Query() {
		for _, v := range va {
			if k == l.SaaSTokenName {
				// logrus.Infof("setting user in session: %s", v)
				token = v
				break
			}
		}
	}
	if reffCk != nil && reffCk.Name != "" {
		reffCk.Expires = time.Now().Add(-2 * time.Second)
		http.SetCookie(w, reffCk)
	}
	session.Values[l.SaaSTokenName] = token
	user, err := l.fetchUserDetails(token)
	if err != nil {
		logrus.Errorf("unable to save session: %v", err)

	}
	session.Values["user"] = user
	err = session.Save(req, w)
	if err != nil {
		logrus.Errorf("unable to save session: %v", err)
	}
	http.Redirect(w, req, reffURL, http.StatusFound)
}

func (l *CloudProvider) fetchUserDetails(tokenVal string) (*User, error) {
	saasURL, _ := url.Parse(l.SaaSBaseURL + "/user")
	req, _ := http.NewRequest(http.MethodGet, saasURL.String(), nil)
	req.AddCookie(&http.Cookie{
		Name:     l.SaaSTokenName,
		Value:    tokenVal,
		Path:     "/",
		HttpOnly: true,
		Domain:   saasURL.Hostname(),
	})
	c := &http.Client{}
	resp, err := c.Do(req)
	if err != nil {
		logrus.Errorf("unable to fetch user data: %v", err)
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	bd, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logrus.Errorf("unable to read body: %v", err)
		return nil, err
	}
	u := &User{}
	err = json.Unmarshal(bd, u)
	if err != nil {
		logrus.Errorf("unable to unmarshal user: %v", err)
		return nil, err
	}
	logrus.Infof("retrieved user: %v", u)
	return u, nil
}

// GetUserDetails - returns the user details
func (l *CloudProvider) GetUserDetails(req *http.Request) (*User, error) {
	// ensuring session is intact before running load test
	session, err := l.GetSession(req)
	if err != nil {
		return nil, err
	}

	user, _ := session.Values["user"].(*User)
	return user, nil
}

// GetSession - returns the session
func (l *CloudProvider) GetSession(req *http.Request) (*sessions.Session, error) {
	session, err := l.SessionStore.Get(req, l.SessionName)
	if err != nil {
		err = errors.Wrap(err, "Error: unable to get session")
		logrus.Error(err)
		return nil, err
	}
	return session, nil
}

// GetProviderToken - returns provider token
func (l *CloudProvider) GetProviderToken(req *http.Request) (string, error) {
	session, err := l.GetSession(req)
	if err != nil {
		return "", err
	}
	tokenVal, _ := session.Values[l.SaaSTokenName].(string)
	return tokenVal, nil
}

// Logout - logout from provider backend
func (l *CloudProvider) Logout(w http.ResponseWriter, req *http.Request) {
	client := http.Client{}
	cReq, err := http.NewRequest(http.MethodGet, l.SaaSBaseURL+"/logout", req.Body)
	if err != nil {
		logrus.Errorf("Error creating a client to logout from tweet app: %v", err)
		http.Error(w, "unable to logout at the moment", http.StatusInternalServerError)
		return
	}
	_, _ = client.Do(cReq)
	// sessionStore.Destroy(w, sessionName)

	sess, err := l.SessionStore.Get(req, l.SessionName)
	if err == nil {
		sess.Options.MaxAge = -1
		_ = sess.Save(req, w)
	}

	http.Redirect(w, req, "/login", http.StatusFound)
}

// FetchResults - fetches results from provider backend
func (l *CloudProvider) FetchResults(req *http.Request, page, pageSize, search, order string) ([]byte, error) {
	logrus.Infof("attempting to fetch results from cloud")
	session, _ := l.GetSession(req)

	tokenVal, _ := session.Values[l.SaaSTokenName].(string)

	saasURL, _ := url.Parse(l.SaaSBaseURL + "/results")
	q := saasURL.Query()
	if page != "" {
		q.Set("page", page)
	}
	if pageSize != "" {
		q.Set("page_size", pageSize)
	}
	if search != "" {
		q.Set("search", search)
	}
	if order != "" {
		q.Set("order", order)
	}
	saasURL.RawQuery = q.Encode()
	logrus.Debugf("constructed results url: %s", saasURL.String())
	cReq, _ := http.NewRequest(http.MethodGet, saasURL.String(), nil)
	cReq.AddCookie(&http.Cookie{
		Name:     l.SaaSTokenName,
		Value:    tokenVal,
		Path:     "/",
		HttpOnly: true,
		Domain:   saasURL.Hostname(),
	})
	c := &http.Client{}
	resp, err := c.Do(cReq)
	if err != nil {
		logrus.Errorf("unable to get results: %v", err)
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	bdr, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logrus.Errorf("unable to read response body: %v", err)
		return nil, err
	}

	if resp.StatusCode == http.StatusOK {
		logrus.Infof("results successfully retrieved from SaaS")
		return bdr, nil
	}
	logrus.Errorf("error while fetching results: %s", bdr)
	return nil, fmt.Errorf("error while sending results - Status code: %d, Body: %s", resp.StatusCode, bdr)
}

// PublishResults - publishes results to the provider backend syncronously
func (l *CloudProvider) PublishResults(req *http.Request, data []byte) (string, error) {
	logrus.Infof("attempting to publish results to SaaS")
	bf := bytes.NewBuffer(data)
	session, _ := l.GetSession(req)

	tokenVal, _ := session.Values[l.SaaSTokenName].(string)

	saasURL, _ := url.Parse(l.SaaSBaseURL + "/result")
	cReq, _ := http.NewRequest(http.MethodPost, saasURL.String(), bf)
	cReq.AddCookie(&http.Cookie{
		Name:     l.SaaSTokenName,
		Value:    tokenVal,
		Path:     "/",
		HttpOnly: true,
		Domain:   saasURL.Hostname(),
	})
	c := &http.Client{}
	resp, err := c.Do(cReq)
	if err != nil {
		logrus.Errorf("unable to send results: %v", err)
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	bdr, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logrus.Errorf("unable to read response body: %v", err)
		return "", err
	}
	if resp.StatusCode == http.StatusCreated {
		logrus.Infof("results successfully published to SaaS")
		idMap := map[string]string{}
		if err = json.Unmarshal(bdr, &idMap); err != nil {
			logrus.Errorf("unable to unmarshal body: %v", err)
			return "", err
		}
		resultID, ok := idMap["id"]
		if ok {
			return resultID, nil
		}
		return "", nil
	}
	logrus.Errorf("error while sending results: %s", bdr)
	return "", fmt.Errorf("error while sending results - Status code: %d, Body: %s", resp.StatusCode, bdr)
}

// PublishMetrics - publishes metrics to the provider backend asyncronously
func (l *CloudProvider) PublishMetrics(tokenVal string, data []byte) error {
	logrus.Infof("attempting to publish metrics to SaaS")
	bf := bytes.NewBuffer(data)

	saasURL, _ := url.Parse(l.SaaSBaseURL + "/result/metrics")
	cReq, _ := http.NewRequest(http.MethodPut, saasURL.String(), bf)
	cReq.AddCookie(&http.Cookie{
		Name:     l.SaaSTokenName,
		Value:    tokenVal,
		Path:     "/",
		HttpOnly: true,
		Domain:   saasURL.Hostname(),
	})
	c := &http.Client{}
	resp, err := c.Do(cReq)
	if err != nil {
		logrus.Errorf("unable to send metrics: %v", err)
		return err
	}
	if resp.StatusCode == http.StatusOK {
		logrus.Infof("metrics successfully published to SaaS")
		return nil
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	bdr, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logrus.Errorf("unable to read response body: %v", err)
		return err
	}
	logrus.Errorf("error while sending metrics: %s", bdr)
	return fmt.Errorf("error while sending metrics - Status code: %d, Body: %s", resp.StatusCode, bdr)
}