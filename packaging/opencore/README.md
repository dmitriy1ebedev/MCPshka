# Open core overlay

`packaging/opencore/public` holds files that replace or add paths in the
public export only (`opencore.Export`, `overlay` in .mcpshka/opencore.json):
the CI each public site runs on a push to main. The private repository never
runs them. Every file there lands at the same path in the public tree, so no
README or other top-level document belongs in it.
