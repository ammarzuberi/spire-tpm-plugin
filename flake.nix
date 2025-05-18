{
  description = "spire-tpm-plugin";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      allSystems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs allSystems (system: f {
        pkgs = import nixpkgs { inherit system; };
      });
    in
    {
      packages = forAllSystems ({ pkgs }: {
        default = pkgs.buildGo124Module rec {
          pname = "spire-tpm-plugin";
          version = "1.0.0";
          doCheck = false;
          src = ./.;
          vendorHash = "sha256-lUpCKWjgVsn0/a1rijiLzrer3rfCn5SDkBAfTYgw0aU=";
        };
      });
    };
}