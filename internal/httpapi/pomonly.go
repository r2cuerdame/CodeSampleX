package httpapi

import "strings"

// gradlePluginMarkerSuffix is how Gradle names the marker it publishes for a
// plugin id. The artifactId is the plugin id with this appended, and the
// artifact behind it is always a lone pom pointing at the implementation.
const gradlePluginMarkerSuffix = ".gradle.plugin"

// mavenPomOnlyByName reports whether a maven coordinate is one whose name
// proves there is no jar behind it.
//
// The name has to be proof and not a hint, because a false exclusion loses
// real authoring work permanently and silently. Gradle's marker convention
// qualifies: it is generated, not chosen, and a marker without a pom-only
// artifact behind it would be a broken publication.
//
// Conventions that merely SUGGEST pom packaging — "-bom", "-parent" — are
// deliberately not here. They are chosen by authors, plenty of them ship a
// jar, and excluding them would throw away work on a guess.
//
// The coordinate arrives as "groupId/artifactId"; a name without a slash is
// read whole, which is what a caller that already split it would pass.
func mavenPomOnlyByName(name string) bool {
	artifact := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		artifact = name[i+1:]
	}
	return strings.HasSuffix(artifact, gradlePluginMarkerSuffix)
}
