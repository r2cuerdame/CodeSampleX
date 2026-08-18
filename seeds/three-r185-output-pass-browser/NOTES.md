# three.js r185 OutputPass follows renderer state

`OutputPass` does not snapshot the renderer's output settings in its
constructor. On each render it copies `toneMappingExposure` into the shader
uniform. When the tone-mapping algorithm or output color space changes, it
rebuilds the relevant shader defines on the next render.

The contract serves only local files, launches the verifier's pinned Chrome
134, obtains a real WebGL 2 context, and renders the pass with the container
network disabled.
