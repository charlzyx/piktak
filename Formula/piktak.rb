class Piktak < Formula
  desc "Connect remote clients to services running on your machine"
  homepage "https://github.com/charlzyx/piktak"
  version "0.1.1"
  license :cannot_represent

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.1/piktakd-darwin-arm64"
      sha256 "1e03dd8c795cb5405fb7f741c31b307f83dfc11d0be9291bec150bce608c159a"
    else
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.1/piktakd-darwin-amd64"
      sha256 "917904cc4622e5d07a22a2ee03a38b2c901b7beba5250d790df52a703f51f788"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.1/piktakd-linux-arm64"
      sha256 "0b9d28e24b076d32678d0c7eaeb94f7d0027dff21cdbc86d15ac563f766ae252"
    else
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.1/piktakd-linux-amd64"
      sha256 "01add7572ba6856a1928bb7193f5c5fd18846f9806caf3556879edd82786172f"
    end
  end

  def install
    bin.install Dir["piktakd-*"].first => "piktakd"
  end

  service do
    run [opt_bin/"piktakd", "-config", "#{Dir.home}/.config/piktak/config.yml"]
    keep_alive true
  end

  test do
    assert_predicate bin/"piktakd", :executable?
  end
end
