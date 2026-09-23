// A collage plugin that strips whitespace and comments from what the application
// serves. It is a separate module because a plugin is a separate project: it
// requires collage the way any consumer would, and nothing about living in the
// framework's repository gives it access the framework does not give everyone.
module github.com/Elagoht/collage-minimizer

go 1.26

require github.com/Elagoht/collage v0.1.0

// Until collage has a released tag, this points the require at a checkout beside
// this one. Delete it once there is a version to fetch — it is the only line in
// this file that assumes anything about where the framework lives on disk.
replace github.com/Elagoht/collage => ../collage
