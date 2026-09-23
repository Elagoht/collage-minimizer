// A collage plugin that strips whitespace and comments from what an application
// serves: rendered pages, documents, and mounted assets.
//
// It requires collage the way any consumer does. Nothing here reaches the
// framework's internals, and nothing could: Go does not let one module import
// another's internal packages, which is the same wall every third-party plugin
// stands behind.
module github.com/Elagoht/collage-minimizer

go 1.26

require github.com/Elagoht/collage v0.2.0
