# snip's nix package. The canonical source is THIS repo (github:ZacxDev/snip);
# a consumer that wants desktop integration adds its own wrapper on top (see
# devrc's nix/pkgs/tools/snip/default.nix for the worked example: a launcher
# that captures the origin window, exports SNIP_REPO, and joins the binaries).
#
# 🔴 THE VERSION IS READ OUT OF THE SOURCE BEING COMPILED. IT IS NOT WRITTEN
# HERE. The same failure produced this mechanism in devrc's local build: a
# hand-maintained `version = "x.y.z"` literal was a claim about code nothing
# kept in step, and it once stamped `0.7.95` onto a binary built from `0.7.87`
# source. version.go and the release that tags it move in the same commit, so
# reading the value out of the tree cannot drift.
#
# 🔴 AN UNPARSEABLE SOURCE THROWS AT EVAL — deliberately the OPPOSITE of the
# local-package convention (evaluate to null, skip the install, stay loud at
# use time). A flake's evaluation surface IS the build: `nix build` /
# `nix flake check` fail here, visibly, naming the file — and version.go's
# declaration shape is pinned by cmd/snip's TestVersionDeclarationShape, so a
# reformat that breaks this parse is caught by the module's own tests first.
{
  lib,
  buildGoModule,
  makeWrapper,
  xclip,
  xdotool,
  dunst,
  bash,
  neovim,
  src,
}:

let
  versionPattern = "var buildVersion = \"([^\"]+)\".*";
  versionLines = builtins.filter
    (l: builtins.isString l && builtins.match versionPattern l != null)
    (builtins.split "\n" (builtins.readFile (src + "/cmd/snip/version.go")));
  parsedVersion =
    if builtins.length versionLines == 1
    then builtins.head (builtins.match versionPattern (builtins.head versionLines))
    else throw "snip/package.nix: expected exactly one 'var buildVersion = \"…\"' line in cmd/snip/version.go, found ${toString (builtins.length versionLines)}";
in
buildGoModule {
  pname = "snip";
  version = parsedVersion;

  src = lib.cleanSource src;

  # 🔴 MEASURED by building with a deliberately wrong hash and taking the
  # "got:" value from the failure. NEVER hand-edited: change go.mod/go.sum
  # and re-derive this the same way.
  vendorHash = "sha256-qpcRouxzBsHE/fSMvwAeuYYq8mRIAzPpsiVfE77z8Y8=";

  subPackages = [ "cmd/snip" ];

  # 🔴 doCheck = false, AND IT IS NOT A CONVENIENCE — the same finding devrc's
  # local build recorded: nixpkgs' Go module builder defaults checks on, and
  # a red Go test would fail every consumer's switch (a home-manager
  # `home.packages` entry is exactly that). The tests run in this repo's CI
  # (GitHub Actions) instead — a red gate must never be able to fail a
  # machine upgrade.
  doCheck = false;

  # 🔴 THE STAMP IS AN IDENTITY — parsedVersion was read out of the very
  # `var buildVersion` line this overwrites.
  ldflags = [ "-s" "-w" "-X main.buildVersion=${parsedVersion}" ];

  nativeBuildInputs = [ makeWrapper ];
  postInstall = ''
    wrapProgram $out/bin/snip \
      --prefix PATH : ${lib.makeBinPath [
        xclip    # clipboard save/set/restore (internal/effects)
        xdotool  # picker-gone probe + paste key + origin name
        dunst    # dunstify failure toasts
        bash     # the ctrl+e landing exec (`bash <landing-script>`)
        neovim   # the ctrl+e editor's documented default ($SNIP_EDITOR wins)
      ]}
  '';

  # NOT pinned here, on purpose:
  #   i3     — internal/effects execs i3-msg but falls back to xdotool; a
  #            non-i3 user must not inherit i3 in their closure.
  #   git    — only the landing script needs it, and the landing script is
  #            the consumer's (SNIP_REPO); the consumer's wrapper pins it.
  #   alacritty — a launcher concern, not the tool's.
  meta = with lib; {
    description = "hotkey → fuzzy snippet picker → paste";
    mainProgram = "snip";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
