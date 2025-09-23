package profile

type Profile uint8

const (
	RFC6726 Profile = iota // FLUTE v2
	RFC3926                // FLUTE v1
)

func (p Profile) String() string {
	switch p {
	case RFC6726:
		return "RFC6726"
	case RFC3926:
		return "RFC3926"
	default:
		return "UNKNOWN"
	}
}
