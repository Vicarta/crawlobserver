package parser

import (
	"sort"
	"strconv"
	"strings"

	"github.com/SEObserver/crawlobserver/internal/htmlutil"
	"golang.org/x/net/html"
)

const (
	maxLinkPositionAncestors = 256
	maxLinkXPathBytes        = 2048
	maxXPathSiblingElements  = 4096
	maxXPathSiblingNodes     = 8192
	maxPositionAttrBytes     = 4096
)

var landmarkTags = map[string]bool{
	"main": true, "article": true, "nav": true,
	"header": true, "footer": true, "aside": true,
}

var landmarkRoles = map[string]string{
	"main": "main", "navigation": "nav", "banner": "header",
	"contentinfo": "footer", "complementary": "aside",
}

type linkPosition struct {
	landmark       string
	xpath          string
	depth          uint16
	documentIndex  uint32
	blockSignature uint64
}

func positionForLink(n *html.Node, cache *xpathIndexCache) (linkPosition, bool) {
	var chain []*html.Node
	for e := n; e != nil && e.Type == html.ElementNode; e = e.Parent {
		if len(chain) >= maxLinkPositionAncestors || len(e.Data) > maxLinkXPathBytes || !validXPathName(e.Data) {
			return linkPosition{}, false
		}
		chain = append(chain, e)
	}
	if len(chain) == 0 {
		return linkPosition{}, false
	}

	xpath, ok := nodeXPath(chain, cache)
	if !ok {
		return linkPosition{}, false
	}
	landmark, ok := landmarkFromChain(chain)
	if !ok {
		return linkPosition{}, false
	}
	signature, ok := blockSignature(chain)
	if !ok {
		return linkPosition{}, false
	}
	return linkPosition{
		landmark:       landmark,
		xpath:          xpath,
		depth:          uint16(len(chain) - 1),
		blockSignature: signature,
	}, true
}

func landmarkFromChain(chain []*html.Node) (string, bool) {
	for _, e := range chain[1:] {
		if landmarkTags[e.Data] {
			return e.Data, true
		}
	}

	roleBytes := 0
	for _, e := range chain[1:] {
		role, ok := htmlutil.NodeAttr(e, "role")
		if !ok {
			continue
		}
		roleBytes += len(role)
		if roleBytes > maxPositionAttrBytes {
			return "", false
		}
		for _, token := range strings.Fields(role) {
			if tag, ok := landmarkRoles[strings.ToLower(token)]; ok {
				return tag, true
			}
		}
	}
	return "", true
}

func nodeXPath(chain []*html.Node, cache *xpathIndexCache) (string, bool) {
	segments := make([]string, 0, len(chain))
	pathBytes := 0
	for _, e := range chain {
		remaining := maxLinkXPathBytes - pathBytes - 1
		if remaining < 0 {
			return "", false
		}
		segment, ok := xpathSegment(e, cache, remaining)
		if !ok {
			return "", false
		}
		pathBytes += len(segment) + 1
		if pathBytes > maxLinkXPathBytes {
			return "", false
		}
		segments = append(segments, segment)
	}

	var b strings.Builder
	b.Grow(pathBytes)
	for i := len(segments) - 1; i >= 0; i-- {
		b.WriteByte('/')
		b.WriteString(segments[i])
	}
	return b.String(), true
}

type xpathIndexCache struct {
	parents       map[*html.Node]xpathParentIndex
	invalidParent map[*html.Node]struct{}
}

type xpathParentIndex struct {
	positions map[*html.Node]int
	counts    map[string]int
}

func newXPathIndexCache() *xpathIndexCache {
	return &xpathIndexCache{
		parents:       make(map[*html.Node]xpathParentIndex),
		invalidParent: make(map[*html.Node]struct{}),
	}
}

func (c *xpathIndexCache) parentIndex(parent *html.Node) (xpathParentIndex, bool) {
	if index, ok := c.parents[parent]; ok {
		return index, true
	}
	if _, invalid := c.invalidParent[parent]; invalid {
		return xpathParentIndex{}, false
	}

	index := xpathParentIndex{
		positions: make(map[*html.Node]int),
		counts:    make(map[string]int),
	}
	elementSiblings := 0
	siblingNodes := 0
	for sibling := parent.FirstChild; sibling != nil; sibling = sibling.NextSibling {
		siblingNodes++
		if siblingNodes > maxXPathSiblingNodes {
			c.invalidParent[parent] = struct{}{}
			return xpathParentIndex{}, false
		}
		if sibling.Type != html.ElementNode {
			continue
		}
		elementSiblings++
		if elementSiblings > maxXPathSiblingElements {
			c.invalidParent[parent] = struct{}{}
			return xpathParentIndex{}, false
		}
		if len(sibling.Data) > maxLinkXPathBytes || !validXPathName(sibling.Data) {
			continue
		}
		index.counts[sibling.Data]++
		index.positions[sibling] = index.counts[sibling.Data]
	}
	c.parents[parent] = index
	return index, true
}

func xpathSegment(e *html.Node, cache *xpathIndexCache, maxBytes int) (string, bool) {
	if len(e.Data) > maxBytes || !validXPathName(e.Data) {
		return "", false
	}
	if e.Parent == nil {
		return e.Data, true
	}

	var position, count int
	if cache != nil {
		index, ok := cache.parentIndex(e.Parent)
		if !ok {
			return "", false
		}
		position, count = index.positions[e], index.counts[e.Data]
	} else {
		elementSiblings := 0
		siblingNodes := 0
		for sibling := e.Parent.FirstChild; sibling != nil; sibling = sibling.NextSibling {
			siblingNodes++
			if siblingNodes > maxXPathSiblingNodes {
				return "", false
			}
			if sibling.Type != html.ElementNode {
				continue
			}
			elementSiblings++
			if elementSiblings > maxXPathSiblingElements {
				return "", false
			}
			if sibling.Data == e.Data {
				count++
				if sibling == e {
					position = count
				}
			}
		}
	}
	if count <= 1 {
		return e.Data, true
	}

	positionText := strconv.Itoa(position)
	if len(e.Data)+len(positionText)+2 > maxBytes {
		return "", false
	}
	return e.Data + "[" + positionText + "]", true
}

func validXPathName(name string) bool {
	if len(name) == 0 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == ':' || c >= 0x80 {
			return false
		}
		if i == 0 {
			if !isNameStart(c) {
				return false
			}
			continue
		}
		if !isNameStart(c) && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

func isNameStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func blockSignature(chain []*html.Node) (uint64, bool) {
	ancestors := make([]*html.Node, 0, len(chain))
	inputBytes := 0
	for _, e := range chain[1:] {
		if e.Data == "body" || e.Data == "html" {
			break
		}
		id, _ := htmlutil.NodeAttr(e, "id")
		class, _ := htmlutil.NodeAttr(e, "class")
		inputBytes += len(e.Data) + len(id) + len(class)
		if inputBytes > maxPositionAttrBytes {
			return 0, false
		}
		ancestors = append(ancestors, e)
	}

	var b strings.Builder
	for i := len(ancestors) - 1; i >= 0; i-- {
		e := ancestors[i]
		id, _ := htmlutil.NodeAttr(e, "id")
		class, _ := htmlutil.NodeAttr(e, "class")
		classes := strings.Fields(class)
		sort.Strings(classes)
		writeSignaturePart(&b, e.Data)
		writeSignaturePart(&b, id)
		writeSignaturePart(&b, strconv.Itoa(len(classes)))
		for _, item := range classes {
			writeSignaturePart(&b, item)
		}
	}
	return hashToken(b.String()), true
}

func writeSignaturePart(b *strings.Builder, value string) {
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte(':')
	b.WriteString(value)
}
