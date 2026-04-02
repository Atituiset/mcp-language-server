package treesitter

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

type ASTNode struct {
	Type     string
	Content  string
	StartRow uint32
	StartCol uint32
	EndRow   uint32
	EndCol   uint32
	Depth    int
	Children []*ASTNode
}

func (n *ASTNode) String() string {
	var b strings.Builder
	n.formatTo(&b, 0)
	return b.String()
}

func (n *ASTNode) formatTo(b *strings.Builder, depth int) {
	indent := strings.Repeat("  ", depth)
	b.WriteString(fmt.Sprintf("%s[%s] %q (L%d:C%d - L%d:C%d)\n",
		indent, n.Type, truncate(n.Content, 50), n.StartRow+1, n.StartCol+1, n.EndRow+1, n.EndCol+1))
	for _, child := range n.Children {
		child.formatTo(b, depth+1)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func TreeToAST(tree *sitter.Tree, source []byte, maxDepth int) *ASTNode {
	return nodeToAST(tree.RootNode(), source, 0, maxDepth)
}

func nodeToAST(node *sitter.Node, source []byte, depth, maxDepth int) *ASTNode {
	if node == nil || depth > maxDepth {
		return nil
	}

	start := node.StartPoint()
	end := node.EndPoint()

	astNode := &ASTNode{
		Type:     node.Type(),
		Content:  node.Content(source),
		StartRow: start.Row,
		StartCol: start.Column,
		EndRow:   end.Row,
		EndCol:   end.Column,
		Depth:    depth,
	}

	if depth < maxDepth {
		childCount := node.ChildCount()
		for i := uint32(0); i < childCount; i++ {
			child := node.Child(int(i))
			childAST := nodeToAST(child, source, depth+1, maxDepth)
			if childAST != nil {
				astNode.Children = append(astNode.Children, childAST)
			}
		}
	}

	return astNode
}

func FilterByType(node *ASTNode, nodeType string) []*ASTNode {
	var results []*ASTNode
	if node.Type == nodeType {
		results = append(results, node)
	}
	for _, child := range node.Children {
		results = append(results, FilterByType(child, nodeType)...)
	}
	return results
}

func FindAllDescendants(node *ASTNode, nodeTypes []string) []*ASTNode {
	typeSet := make(map[string]bool)
	for _, t := range nodeTypes {
		typeSet[t] = true
	}
	return findDescendantsWithFilter(node, typeSet)
}

func findDescendantsWithFilter(node *ASTNode, typeSet map[string]bool) []*ASTNode {
	var results []*ASTNode
	for _, child := range node.Children {
		if typeSet[child.Type] {
			results = append(results, child)
		}
		results = append(results, findDescendantsWithFilter(child, typeSet)...)
	}
	return results
}
