package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

type Cursor struct {
	c      *sitter.TreeCursor
	source []byte
}

func NewCursor(node *sitter.Node, source []byte) *Cursor {
	return &Cursor{
		c:      sitter.NewTreeCursor(node),
		source: source,
	}
}

func (c *Cursor) Close() {
	if c.c != nil {
		c.c.Close()
	}
}

func (c *Cursor) CurrentNode() *sitter.Node {
	return c.c.CurrentNode()
}

func (c *Cursor) CurrentFieldName() string {
	return c.c.CurrentFieldName()
}

func (c *Cursor) GoToFirstChild() bool {
	return c.c.GoToFirstChild()
}

func (c *Cursor) GoToNextSibling() bool {
	return c.c.GoToNextSibling()
}

func (c *Cursor) GoToParent() bool {
	return c.c.GoToParent()
}

func (c *Cursor) Reset(node *sitter.Node) {
	c.c.Reset(node)
}

type CursorIterator struct {
	cursor *Cursor
}

func NewCursorIterator(node *sitter.Node, source []byte) *CursorIterator {
	return &CursorIterator{
		cursor: NewCursor(node, source),
	}
}

func (ci *CursorIterator) Close() {
	ci.cursor.Close()
}

func (ci *CursorIterator) Iterate(callback func(node *sitter.Node, fieldName string, depth int) bool) {
	depth := 0
	ci.cursor.Reset(ci.cursor.CurrentNode())

	if !ci.cursor.GoToFirstChild() {
		return
	}

	for {
		callback(ci.cursor.CurrentNode(), ci.cursor.CurrentFieldName(), depth)

		if ci.cursor.GoToFirstChild() {
			depth++
		} else if !ci.cursor.GoToNextSibling() {
			for {
				if !ci.cursor.GoToParent() {
					return
				}
				depth--
				if ci.cursor.GoToNextSibling() {
					break
				}
			}
		}
	}
}

func (ci *CursorIterator) AllNodes() []*sitter.Node {
	var nodes []*sitter.Node
	ci.Iterate(func(node *sitter.Node, _ string, _ int) bool {
		nodes = append(nodes, node)
		return true
	})
	return nodes
}
