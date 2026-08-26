package state

import "time"

// State — единый снимок состояния агента (контракт GET /v1/state, спека agent-api).
type State struct {
	Container  Container  `json:"container"`
	ExpressVPN ExpressVPN `json:"expressvpn"`
	Uplink     Uplink     `json:"uplink"`
	Proxy      Proxy      `json:"proxy"`
	Killswitch Killswitch `json:"killswitch"`
	Selfcheck  *Selfcheck `json:"selfcheck"`
	LastError  *Error     `json:"lastError"`
	Desired    Desired    `json:"desired"`
}

// Selfcheck — результат последней самопроверки (действие selfcheck).
type Selfcheck struct {
	Verdict      string    `json:"verdict"` // ok | warning | fail
	Reasons      []string  `json:"reasons,omitempty"`
	ProxyIP      string    `json:"proxyIP,omitempty"`
	ProxyCountry string    `json:"proxyCountry,omitempty"`
	UplinkIP     string    `json:"uplinkIP,omitempty"`
	DNS          []string  `json:"dns,omitempty"`
	At           time.Time `json:"at"`
}

type Container struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	// Published* — как порты опубликованы на хосте (из env compose; UI
	// показывает адрес прокси и read-only параметры Container).
	PublishedSocksPort string `json:"publishedSocksPort,omitempty"`
	PublishedHTTPPort  string `json:"publishedHTTPPort,omitempty"`
	PublishedBindAddr  string `json:"publishedBindAddr,omitempty"`
}

// Connection — состояние VPN-подключения.
type Connection string

const (
	ConnDisconnected Connection = "disconnected"
	ConnConnecting   Connection = "connecting"
	ConnConnected    Connection = "connected"
	ConnReconnecting Connection = "reconnecting"
	ConnError        Connection = "error"
)

type Auth string

const (
	AuthLoggedIn  Auth = "logged_in"
	AuthLoggedOut Auth = "logged_out"
)

type ExpressVPN struct {
	Auth       Auth       `json:"auth"`
	Connection Connection `json:"connection"`
	Location   string     `json:"location,omitempty"`
	Protocol   Protocol   `json:"protocol"`
	PublicIP   string     `json:"publicIP,omitempty"`
	// ConnectedAt — момент установления текущего подключения (для длительности в UI).
	ConnectedAt *time.Time `json:"connectedAt,omitempty"`
}

type Protocol struct {
	Requested string `json:"requested"`
	Effective string `json:"effective,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type UplinkStatus string

const (
	UplinkUnknown  UplinkStatus = "unknown"
	UplinkUp       UplinkStatus = "up"
	UplinkDown     UplinkStatus = "down"
	UplinkDegraded UplinkStatus = "degraded"
)

// TriState — udpSupported: true|false|unknown.
type TriState string

const (
	TriUnknown TriState = "unknown"
	TriTrue    TriState = "true"
	TriFalse   TriState = "false"
)

type Uplink struct {
	Mode         string       `json:"mode"`
	Status       UplinkStatus `json:"status"`
	UDPSupported TriState     `json:"udpSupported"`
	// Endpoint — host:port прокси без учётных данных.
	Endpoint string `json:"endpoint,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type Proxy struct {
	Listen      string `json:"listen"`
	Status      string `json:"status"`
	ActiveConns int64  `json:"activeConns"`
	BytesIn     int64  `json:"bytesIn"`
	BytesOut    int64  `json:"bytesOut"`
}

type Killswitch struct {
	Active  bool  `json:"active"`
	Dropped int64 `json:"dropped"`
}

type Error struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// DesiredConnection — желаемое состояние подключения.
type DesiredConnection string

const (
	DesiredConnected    DesiredConnection = "connected"
	DesiredDisconnected DesiredConnection = "disconnected"
)

type Desired struct {
	Connection DesiredConnection `json:"connection"`
}

// Initial — состояние на старте агента.
func Initial(version string) State {
	return State{
		Container: Container{Version: version, StartedAt: time.Now().UTC()},
		ExpressVPN: ExpressVPN{
			Auth:       AuthLoggedOut,
			Connection: ConnDisconnected,
			Protocol:   Protocol{Requested: "auto"},
		},
		Uplink:  Uplink{Mode: "host", Status: UplinkUnknown, UDPSupported: TriUnknown},
		Proxy:   Proxy{Status: "stopped"},
		Desired: Desired{Connection: DesiredDisconnected},
	}
}
