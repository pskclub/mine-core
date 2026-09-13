package models

// The three types below point at each other the way a real location table set
// does: a province holds its districts, a district holds the province it is in,
// and an address holds both. No single path through them repeats a type, so
// refusing to expand a type already on the path does not bound the output —
// every distinct path gets written out, and the count of those grows with the
// graph rather than with its depth.
//
// This is what max_depth exists for, and rendering it is what the generator has
// to stay bounded against.

type Province struct {
	BaseModel
	NameTH string `json:"name_th"`
	NameEN string `json:"name_en"`
	// No omitempty, so a reply carries this key whether or not it was loaded —
	// and the example says so.
	Districts []District `json:"districts"`
	// Tagged the way a relation usually is. An endpoint that does not preload it
	// sends nothing for it, so no example shows it.
	Addresses []Address `json:"addresses,omitempty"`
}

type District struct {
	BaseModel
	NameTH     string    `json:"name_th"`
	ProvinceID string    `json:"province_id"`
	Province   *Province `json:"province"`
	Addresses  []Address `json:"addresses"`
}

type Address struct {
	BaseModel
	Line       string    `json:"line"`
	DistrictID string    `json:"district_id"`
	District   *District `json:"district"`
	Province   *Province `json:"province"`
}
