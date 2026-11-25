package netprov

// ManagedWANIface UCI 里"我们建的"接口（modem<id> / modem<id>_6）
type ManagedWANIface struct {
	Name    string
	ModemID int
	Device  string
	Proto   string
}
