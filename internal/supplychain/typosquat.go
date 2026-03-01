package supplychain

import (
	"strings"
)

// VerdictTyposquat is returned when a package name looks like a typosquat of a popular package.
const VerdictTyposquat Verdict = "typosquat"

// popularNPMPackages is a curated list of widely-used npm packages that are
// frequently targeted by typosquatting attacks.
var popularNPMPackages = []string{
	"express", "react", "react-dom", "lodash", "axios", "chalk",
	"commander", "webpack", "babel", "typescript", "eslint", "prettier",
	"next", "vue", "angular", "jquery", "moment", "underscore",
	"async", "debug", "uuid", "fs-extra", "glob", "minimist",
	"yargs", "semver", "dotenv", "cors", "body-parser", "mongoose",
	"sequelize", "socket.io", "nodemon", "jest", "mocha", "chai",
	"vitest", "vite", "esbuild", "rollup", "parcel",
	"tailwindcss", "postcss", "sass", "less",
	"passport", "jsonwebtoken", "bcrypt", "helmet", "morgan",
	"puppeteer", "cheerio", "sharp", "multer",
	"redis", "pg", "mysql2", "mongodb", "prisma",
	"graphql", "apollo-server", "fastify", "koa", "hapi",
	"dayjs", "date-fns", "luxon", "ramda", "immutable",
	"rxjs", "zod", "joi", "ajv", "class-validator",
}

// popularPyPIPackages is a curated list of widely-used PyPI packages.
var popularPyPIPackages = []string{
	"requests", "flask", "django", "numpy", "pandas", "scipy",
	"matplotlib", "scikit-learn", "tensorflow", "pytorch", "torch",
	"transformers", "fastapi", "uvicorn", "gunicorn", "celery",
	"sqlalchemy", "alembic", "pydantic", "pytest", "coverage",
	"black", "ruff", "mypy", "pylint", "flake8",
	"boto3", "botocore", "awscli", "google-cloud-storage",
	"cryptography", "paramiko", "fabric", "ansible",
	"pillow", "opencv-python", "beautifulsoup4", "scrapy",
	"httpx", "aiohttp", "urllib3", "certifi",
	"click", "typer", "rich", "tqdm", "colorama",
	"jinja2", "mako", "pyyaml", "toml", "configparser",
	"redis", "pymongo", "psycopg2", "mysql-connector-python",
	"setuptools", "wheel", "pip", "virtualenv", "poetry",
	"langchain", "openai", "anthropic", "tiktoken",
	"huggingface-hub", "datasets", "tokenizers", "accelerate",
}

// TyposquatResult holds the outcome of a typosquat check.
type TyposquatResult struct {
	IsTyposquat bool   // True if the name looks like a typosquat
	SimilarTo   string // The popular package it resembles
	Distance    int    // Levenshtein edit distance
}

// CheckTyposquat checks whether a package name looks like a typosquat of a
// popular package. It uses Levenshtein distance with a threshold of 1--2 edits
// depending on package name length.
func CheckTyposquat(name string, registry Registry) TyposquatResult {
	name = strings.ToLower(name)

	var popular []string
	switch registry {
	case RegistryNPM:
		popular = popularNPMPackages
	case RegistryPyPI:
		popular = popularPyPIPackages
	default:
		return TyposquatResult{}
	}

	// Short names (<=3 chars) are too common to reliably detect typosquats
	if len(name) <= 3 {
		return TyposquatResult{}
	}

	maxDist := 1
	if len(name) >= 8 {
		maxDist = 2
	}

	bestDist := maxDist + 1
	bestMatch := ""
	for _, pkg := range popular {
		if name == pkg {
			// Exact match means it IS the popular package, not a typosquat
			return TyposquatResult{}
		}

		// Quick length-difference pruning: if lengths differ by more than
		// maxDist, Levenshtein distance must exceed maxDist.
		if abs(len(name)-len(pkg)) > maxDist {
			continue
		}

		d := levenshtein(name, pkg)
		if d <= maxDist && d < bestDist {
			bestDist = d
			bestMatch = pkg
		}
	}

	if bestMatch != "" {
		return TyposquatResult{
			IsTyposquat: true,
			SimilarTo:   bestMatch,
			Distance:    bestDist,
		}
	}

	return TyposquatResult{}
}

// levenshtein computes the Levenshtein edit distance between two strings.
// This is a straightforward two-row dynamic programming implementation.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	// Use two rows of the DP matrix to save memory.
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev, curr = curr, prev
	}

	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
