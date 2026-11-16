package mcp

import "strings"

// Match the qualified Squad 1.1.0 catalog before native writes.
func squadDisplayRole(role string) string {
	// Only the Kelvin sign lowercases from non-ASCII into the pinned catalog.
	id := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r == 'K' {
			return 'k'
		}
		return r
	}, role)
	switch strings.Join(strings.Fields(id), "-") {
	case "lead":
		return "Lead / Architect"
	case "frontend":
		return "Frontend Developer"
	case "backend":
		return "Backend Developer"
	case "fullstack":
		return "Full-Stack Developer"
	case "reviewer":
		return "Code Reviewer"
	case "tester":
		return "Test Engineer"
	case "devops":
		return "DevOps Engineer"
	case "security":
		return "Security Engineer"
	case "data":
		return "Data Engineer"
	case "docs":
		return "Technical Writer"
	case "ai":
		return "AI / ML Engineer"
	case "designer":
		return "UI/UX Designer"
	case "fact-checker":
		return "Fact Checker"
	case "marketing-strategist":
		return "Marketing Strategist"
	case "sales-strategist":
		return "Sales Strategist"
	case "product-manager":
		return "Product Manager"
	case "project-manager":
		return "Project Manager"
	case "support-specialist":
		return "Support Specialist"
	case "game-developer":
		return "Game Developer"
	case "media-buyer":
		return "Media Buyer"
	case "compliance-legal":
		return "Compliance & Legal"
	}
	return role
}
