{
  description = "snip — hotkey → fuzzy snippet picker → paste";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      # The package is defined in package.nix (its header carries the version
      # and check-phase reasoning). Callers may also consume this overlay.
      overlays.default = final: _prev: {
        snip = final.callPackage ./package.nix { src = self; };
      };

      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        rec {
          snip = pkgs.callPackage ./package.nix { src = self; };
          default = snip;
        }
      );

      apps = forAllSystems (system: rec {
        snip = {
          type = "app";
          program = "${self.packages.${system}.snip}/bin/snip";
        };
        default = snip;
      });
    };
}
