package wafg

import (
	"net/http"
	"sync"
	_ "expvar"
	"time"
)

type WafServer struct {
	sync.RWMutex
	Settings      Settings
	remoteClients map[string]*RemoteClient
	IpBanManager  *IpBanManager
	httpCLient    *http.Client
	Callbacks     *Callbacks
	Rules         *RulesManager
}

/**************************/
func (ws *WafServer) ServeForbidden(ctx *Context) {
	perfCounters.Add(COUNTER_BLOCKED_CONNECTIONS, 1)
	w := *ctx.OrigWriter
	w.WriteHeader(403)
	w.Write([]byte("Forbidden"))
	ctx.Data.RespCode = 403
}

//todo: remove old clients

//analyze the request
func (ws *WafServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	perfCounters.Add(COUNTER_CONNECTIONS, 1)
	
	ctx := newContext(&w, r)
	defer ws.triggerAfterServed(ctx)
	
	//get appropriate pagerules
	rulesSet := ws.Rules.GetMatchedRules(ctx)
	
	//If client is whitelisted, proceed with request ignoring everything else
	if ws.IpBanManager.IsWhiteListed(ctx.Ip) || ws.Rules.RulesetHasAction(rulesSet, actionWhitelist) {
		perfCounters.Add(COUNTER_WHITELISTED_CONNECTIONS, 1)
		ws.proceed(ctx)
		return
	}
	
	//get the ip and check if we are banned already
	if ws.IpBanManager.IsBlocked(ctx.Ip) || ws.Rules.RulesetHasAction(rulesSet, actionForbid) {
		log.DebugfWithFields("Refused connection", LogFields{"ip": ctx.Ip})
		ws.ServeForbidden(ctx)
		return
	}
	
	//get the client or create it if it doesn't exists
	client := ws.getClient(ctx.Ip)
	if !client.CanServe(ctx, rulesSet) {
		ws.ServeForbidden(ctx)
		return
	}
	
	//we are good to go
	ctx.Timers.BeginRequest = time.Now()
	ws.proceed(ctx)
}

// We have passed all checks, proceed with request.
func (ws *WafServer) proceed(ctx *Context) {
	ctx.Refused = false
	//create reverse proxy and execute request
	logRequest(ctx)
	
	mhrp := NewMultiHostReverseProxy(ctx.OrigRequest)
	mhrp.Transport = ws.httpCLient.Transport
	mhrp.ServeHTTP(ctx)
}

//Triggers callbacks after request has been served
func (ws *WafServer) triggerAfterServed(ctx *Context) {
	callbacks := ws.Callbacks.getAfterServedCallbacks()
	if len(callbacks) > 0 {
		for _, f := range callbacks {
			f(ctx)
		}
	}
}

//
func (ws *WafServer) triggerAfterResponse(ctx *Context, response *http.Response) {
	callbacks := ws.Callbacks.getAfterResponseCallbacks()
	if len(callbacks) > 0 {
		for _, f := range callbacks {
			f(ctx, response)
		}
	}
}

//Gets (or creates) client from cache
func (ws *WafServer) getClient(ip string) *RemoteClient {
	ws.Lock()
	defer ws.Unlock()
	var client *RemoteClient
	
	client, ok := ws.remoteClients[ip]
	if !ok {
		client = createNewRemoteClient(ip)
		ws.remoteClients[ip] = client
	}
	return client
}

func logRequest(ctx *Context) {
	ctx.Timers.Served = time.Now()
	
	log.Debugf(
		`%s - [%s] - %s %s [%dms] [%dns]`,
		ctx.Ip,
		time.Now().Format("2006-01-02 15:04:05"),
		ctx.Data.Method,
		ctx.Data.Path,
		ctx.GetTotalTime().Nanoseconds()/1e6,
		ctx.GetOverhead().Nanoseconds(),
	)
}
