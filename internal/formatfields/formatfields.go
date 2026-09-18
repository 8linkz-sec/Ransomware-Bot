package formatfields

import "strings"

const (
	FieldID               = "id"
	FieldCountry          = "country"
	FieldVictim           = "victim"
	FieldGroup            = "group"
	FieldActivity         = "activity"
	FieldAttackDate       = "attack_date"
	FieldAttackDateLegacy = "attackdate"
	FieldDiscovered       = "discovered"
	FieldPublished        = "published"
	FieldPostURL          = "post_url"
	FieldClaimURL         = "claim_url"
	FieldWebsite          = "website"
	FieldURL              = "url"
	FieldDescription      = "description"
	FieldScreenshot       = "screenshot"
)

const ValidFieldNames = FieldID + ", " +
	FieldCountry + ", " +
	FieldVictim + ", " +
	FieldGroup + ", " +
	FieldActivity + ", " +
	FieldAttackDate + ", " +
	FieldDiscovered + ", " +
	FieldPublished + ", " +
	FieldPostURL + ", " +
	FieldWebsite + ", " +
	FieldURL + ", " +
	FieldDescription + ", " +
	FieldScreenshot
const ActionLinkFieldNames = "post_url, website, url, or screenshot"

const (
	RSSFieldTitle       = "title"
	RSSFieldDescription = "description"
	RSSFieldLink        = "link"
	RSSFieldAuthor      = "author"
	RSSFieldCategories  = "categories"
	RSSFieldPublished   = "published"
	RSSFieldFeedTitle   = "feed_title"
	RSSFieldFeedURL     = "feed_url"
)

const ValidRSSFieldNames = "title, description, link, author, categories, published, feed_title, source, feed_url"

var defaultDiscordFieldOrder = []string{
	FieldGroup,
	FieldVictim,
	FieldCountry,
	FieldActivity,
	FieldDiscovered,
	FieldPostURL,
}

var defaultSlackFieldOrder = []string{
	FieldGroup,
	FieldVictim,
	FieldCountry,
	FieldActivity,
	FieldDiscovered,
	FieldPostURL,
}

var defaultRSSFieldOrder = []string{
	RSSFieldTitle,
	RSSFieldDescription,
	RSSFieldLink,
	RSSFieldAuthor,
	RSSFieldCategories,
	RSSFieldPublished,
	RSSFieldFeedTitle,
}

func DefaultDiscordFieldOrder() []string {
	return append([]string(nil), defaultDiscordFieldOrder...)
}

func DefaultSlackFieldOrder() []string {
	return append([]string(nil), defaultSlackFieldOrder...)
}

func DefaultRSSFieldOrder() []string {
	return append([]string(nil), defaultRSSFieldOrder...)
}

func Normalize(field string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case FieldID:
		return FieldID, true
	case FieldCountry:
		return FieldCountry, true
	case FieldVictim:
		return FieldVictim, true
	case FieldGroup:
		return FieldGroup, true
	case FieldActivity:
		return FieldActivity, true
	case FieldAttackDate, FieldAttackDateLegacy:
		return FieldAttackDate, true
	case FieldDiscovered:
		return FieldDiscovered, true
	case FieldPublished:
		return FieldPublished, true
	case FieldDescription:
		return FieldDescription, true
	case FieldScreenshot:
		return FieldScreenshot, true
	case FieldPostURL, FieldClaimURL:
		return FieldPostURL, true
	case FieldWebsite, FieldURL:
		return FieldWebsite, true
	default:
		return "", false
	}
}

func NormalizeRSS(field string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case RSSFieldTitle:
		return RSSFieldTitle, true
	case RSSFieldDescription:
		return RSSFieldDescription, true
	case RSSFieldLink:
		return RSSFieldLink, true
	case RSSFieldAuthor:
		return RSSFieldAuthor, true
	case RSSFieldCategories, "category":
		return RSSFieldCategories, true
	case RSSFieldPublished, "published_at":
		return RSSFieldPublished, true
	case RSSFieldFeedTitle, "source":
		return RSSFieldFeedTitle, true
	case RSSFieldFeedURL:
		return RSSFieldFeedURL, true
	default:
		return "", false
	}
}

func RSSFieldLabel(canonicalField string) string {
	switch canonicalField {
	case RSSFieldTitle:
		return "Title"
	case RSSFieldDescription:
		return "Description"
	case RSSFieldLink:
		return "Link"
	case RSSFieldAuthor:
		return "Author"
	case RSSFieldCategories:
		return "Categories"
	case RSSFieldPublished:
		return "Published"
	case RSSFieldFeedTitle:
		return "Source"
	case RSSFieldFeedURL:
		return "Feed URL"
	default:
		return canonicalField
	}
}

func IsValid(field string) bool {
	_, ok := Normalize(field)
	return ok
}

func IsActionLink(canonicalField string) bool {
	switch canonicalField {
	case FieldPostURL, FieldWebsite, FieldScreenshot:
		return true
	default:
		return false
	}
}
