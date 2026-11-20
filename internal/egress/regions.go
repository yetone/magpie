package egress

// Supported is Anthropic's Supported Regions Policy
// (https://www.anthropic.com/supported-countries, effective 2026-11-12,
// read 2026-11-21): every country and region it lists, by ISO 3166-1
// code, with the name the page gives. testdata/supported-regions.txt is
// the page's list as it was read, and a test holds this map to it.
var Supported = map[string]string{
	"AL": "Albania", "DZ": "Algeria", "AD": "Andorra", "AO": "Angola",
	"AG": "Antigua and Barbuda", "AR": "Argentina", "AM": "Armenia",
	"AU": "Australia", "AT": "Austria", "AZ": "Azerbaijan", "BS": "Bahamas",
	"BH": "Bahrain", "BD": "Bangladesh", "BB": "Barbados", "BE": "Belgium",
	"BZ": "Belize", "BJ": "Benin", "BT": "Bhutan", "BO": "Bolivia",
	"BA": "Bosnia and Herzegovina", "BW": "Botswana", "BR": "Brazil",
	"BN": "Brunei", "BG": "Bulgaria", "BF": "Burkina Faso", "BI": "Burundi",
	"CV": "Cabo Verde", "KH": "Cambodia", "CM": "Cameroon", "CA": "Canada",
	"CF": "Central African Republic", "TD": "Chad", "CL": "Chile",
	"CO": "Colombia", "KM": "Comoros", "CG": "Congo (Brazzaville)",
	"CD": "Congo, Democratic Republic of (DRC)", "CR": "Costa Rica",
	"CI": "Côte d'Ivoire", "HR": "Croatia", "CY": "Cyprus",
	"CZ": "Czechia (Czech Republic)", "DK": "Denmark", "DJ": "Djibouti",
	"DM": "Dominica", "DO": "Dominican Republic", "EC": "Ecuador",
	"EG": "Egypt", "SV": "El Salvador", "GQ": "Equatorial Guinea",
	"ER": "Eritrea", "EE": "Estonia", "SZ": "Eswatini", "ET": "Ethiopia",
	"FJ": "Fiji", "FI": "Finland", "FR": "France", "GA": "Gabon",
	"GM": "Gambia", "GE": "Georgia", "DE": "Germany", "GH": "Ghana",
	"GR": "Greece", "GD": "Grenada", "GT": "Guatemala", "GN": "Guinea",
	"GW": "Guinea-Bissau", "GY": "Guyana", "HT": "Haiti", "HN": "Honduras",
	"HU": "Hungary", "IS": "Iceland", "IN": "India", "ID": "Indonesia",
	"IQ": "Iraq", "IE": "Ireland", "IL": "Israel", "IT": "Italy",
	"JM": "Jamaica", "JP": "Japan", "JO": "Jordan", "KZ": "Kazakhstan",
	"KE": "Kenya", "KI": "Kiribati", "KW": "Kuwait", "KG": "Kyrgyzstan",
	"LA": "Laos", "LV": "Latvia", "LB": "Lebanon", "LS": "Lesotho",
	"LR": "Liberia", "LY": "Libya", "LI": "Liechtenstein", "LT": "Lithuania",
	"LU": "Luxembourg", "MG": "Madagascar", "MW": "Malawi", "MY": "Malaysia",
	"MV": "Maldives", "ML": "Mali", "MT": "Malta", "MH": "Marshall Islands",
	"MR": "Mauritania", "MU": "Mauritius", "MX": "Mexico",
	"FM": "Micronesia", "MD": "Moldova", "MC": "Monaco", "MN": "Mongolia",
	"ME": "Montenegro", "MA": "Morocco", "MZ": "Mozambique", "NA": "Namibia",
	"NR": "Nauru", "NP": "Nepal", "NL": "Netherlands", "NZ": "New Zealand",
	"NI": "Nicaragua", "NE": "Niger", "NG": "Nigeria",
	"MK": "North Macedonia", "NO": "Norway", "OM": "Oman", "PK": "Pakistan",
	"PW": "Palau", "PS": "Palestine", "PA": "Panama",
	"PG": "Papua New Guinea", "PY": "Paraguay", "PE": "Peru",
	"PH": "Philippines", "PL": "Poland", "PT": "Portugal", "QA": "Qatar",
	"RO": "Romania", "RW": "Rwanda", "KN": "Saint Kitts and Nevis",
	"LC": "Saint Lucia", "VC": "Saint Vincent and the Grenadines",
	"WS": "Samoa", "SM": "San Marino", "ST": "São Tomé and Príncipe",
	"SA": "Saudi Arabia", "SN": "Senegal", "RS": "Serbia",
	"SC": "Seychelles", "SL": "Sierra Leone", "SG": "Singapore",
	"SK": "Slovakia", "SI": "Slovenia", "SO": "Somalia",
	"SB": "Solomon Islands", "ZA": "South Africa", "KR": "South Korea",
	"SS": "South Sudan", "ES": "Spain", "LK": "Sri Lanka", "SD": "Sudan",
	"SR": "Suriname", "SE": "Sweden", "CH": "Switzerland", "TW": "Taiwan",
	"TJ": "Tajikistan", "TZ": "Tanzania", "TH": "Thailand",
	"TL": "Timor-Leste", "TG": "Togo", "TO": "Tonga",
	"TT": "Trinidad and Tobago", "TN": "Tunisia", "TR": "Türkiye (Turkey)",
	"TM": "Turkmenistan", "TV": "Tuvalu", "UG": "Uganda",
	"UA": "Ukraine (except Crimea, Donetsk, Kherson, Luhansk, and Zaporizhzhia regions)",
	"AE": "United Arab Emirates", "GB": "United Kingdom",
	"US": "United States of America", "UY": "Uruguay", "UZ": "Uzbekistan",
	"VU": "Vanuatu", "VA": "Vatican City", "VN": "Vietnam", "ZM": "Zambia",
	"ZW": "Zimbabwe",
}

// territories are the territories and dependencies of a listed country,
// which the policy supports with it ("for example, Puerto Rico as a
// territory of the United States"), by their own ISO code, as a lookup
// names them. Hong Kong and Macao are China's, which isn't listed.
var territories = map[string]string{
	// the United States'
	"PR": "US", "GU": "US", "VI": "US", "AS": "US", "MP": "US", "UM": "US",
	// the United Kingdom's, and the Crown Dependencies
	"GI": "GB", "BM": "GB", "KY": "GB", "VG": "GB", "AI": "GB", "MS": "GB",
	"TC": "GB", "FK": "GB", "IO": "GB", "SH": "GB", "PN": "GB", "GS": "GB",
	"GG": "GB", "JE": "GB", "IM": "GB",
	// France's
	"GF": "FR", "GP": "FR", "MQ": "FR", "RE": "FR", "YT": "FR", "PM": "FR",
	"BL": "FR", "MF": "FR", "PF": "FR", "NC": "FR", "WF": "FR", "TF": "FR",
	// the Netherlands'
	"AW": "NL", "CW": "NL", "SX": "NL", "BQ": "NL",
	// Denmark's, Norway's, Finland's
	"GL": "DK", "FO": "DK", "SJ": "NO", "BV": "NO", "AX": "FI",
	// Australia's and New Zealand's
	"CX": "AU", "CC": "AU", "NF": "AU", "HM": "AU", "TK": "NZ", "CK": "NZ",
	"NU": "NZ",
}

// supportedLoc says whether a lookup's two-letter code is in a supported
// region: listed, or a listed country's territory.
func supportedLoc(loc string) bool {
	if _, ok := Supported[loc]; ok {
		return true
	}
	_, ok := Supported[territories[loc]]
	return ok
}
