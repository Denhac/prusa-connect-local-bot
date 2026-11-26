package domain

import "strings"

// passwordWords are what a made password is drawn from: short, common words
// that are easy to read off a screen and type, written for this module. Three
// of them give about 30 bits, enough for a first password on a server reached
// only on the space's own network, whose sign-in is rate limited, and which the
// person is asked to change at once. words_test.go holds the list to its rules.
var passwordWords = strings.Fields(wordList)

const wordList = `
acorn actor adobe agent alarm album alder algae alley almond alpine amber anchor angle
ankle anvil apple apricot april apron arbor arch arena armor arrow artist aspen atlas
atom attic autumn avocado award axle bacon badge bagel baker balcony ballad bamboo
banana banjo banner barley barn barrel basil basin basket batch beach beacon beagle beam
bean bear beaver bedrock beech beetle bell bench berry bicycle bison blanket blazer
blender blossom blue bluff board boat bobcat bonnet bonus book boot border bottle
boulder bowl box branch brass bread breeze brick bridge brook broom brush bubble bucket
buckle budget buffalo bugle bundle bunny burrow butter button buzzard cabin cable cactus
cafe cake calf camel camera camp canal candle candy canoe canvas canyon cape captain
carbon cargo carpet carrot cart cartoon castle cedar celery cellar cement chair chalk
channel chapel cheese cherry chess chest chicken chime chimney chip chord cider cinema
circle circus citrus clam clay cliff clock cloud clover coach coast cobalt cocoa coconut
coffee coin comet compass condor copper coral cork corn cottage cotton cougar cousin
coyote crab cradle crane crater crayon cream creek cricket crown crystal cube cupcake
curtain cushion cycle cypress daisy dance dawn deer delta denim desert desk dew diamond
diary dinner dolphin domino donkey door dove dragon drawer dream drum duck dune dust
eagle easel echo eclipse eel egg elbow elder elk elm ember emerald engine envoy epic
equator errand estate evening fable fabric falcon fan farm feather fence fern ferry
festival fiddle field fig film finch fire fjord flag flame flannel flask fleet flint
flower flute foam fog folder forest fork fossil fountain fox frame freckle fridge frog
frost fudge furnace gadget galaxy gallery garden garlic garnet gate gazelle gecko gem
geyser ginger giraffe glacier glass glider globe glove goat gondola goose gopher gourd
grain granite grape graph grass gravel griddle grove guitar gull gust habit hammer
hamster harbor harp harvest hatch hawk hazel heart hearth hedge helmet hen herb heron
hickory hill hinge hippo hobby holly honey hoof horizon hornet horse hotel hound house
hummus hut ice icicle igloo index ink inlet insect iris iron island ivory ivy jacket
jaguar jam jar jasmine jazz jelly jersey jet jewel jigsaw journal journey juice jungle
juniper kayak kelp kennel kernel kettle key kite kitten kiwi knob koala label ladder
ladle lagoon lake lamb lamp lantern lark laser lava lawn leaf ledge lemon lens lentil
letter lettuce lily lime linen lion lizard llama lobster locket lodge loft lotus lumber
lunar lunch lynx magnet mango mantle maple marble market marsh mason meadow melon mesa
metal meteor midnight mile mill mint mirror mist mitten moat module mole monkey moon
moose morning mortar moss moth motor mountain mouse muffin mural museum mushroom music
mustard napkin nectar needle nest nickel night noodle north notch notebook nugget nut
nutmeg oak oasis oat ocean octave octopus olive omelet onion opal orange orbit orchard
orchid organ otter oval oven owl oyster paddle page paint palace palm pancake panda
panel papaya paper parade parcel park parrot pasta pastry path patio peach peanut pear
pebble pecan pelican pencil penguin pepper piano pickle picnic pigeon pillow pilot pine
pinto pioneer pipe pirate pitcher pixel pizza planet plank plaza plum pocket poem polar
pony popcorn poppy porch possum potato pottery prairie pretzel prism pudding puffin
pulley pumpkin puppet puzzle quail quartz quest quill quilt quiver rabbit raccoon radar
radio radish raft rain rainbow raisin rake ranch raven ravine reed reef ribbon rice
ridge river road robin rocket rodeo roof rooster root rope rose rover ruby rudder rug
saddle saffron sage sail salad salmon salt sand sandal satchel saucer scarf school
scooter sea seal season seed shadow shell shelf shore shovel shrimp sierra signal silk
silver siren sketch sky sled sleet slope snail snow soap socket sofa soil solar sonnet
soup spade spark sparrow spice spider spinach sponge spoon spring sprout spruce squash
squid stable stamp star statue steam stone stool storm stove straw stream street studio
sugar summer summit sun sunset swallow swan sweater syrup table taco tadpole tango tank
tea teapot temple tent thistle thunder ticket tide tiger timber toast toffee tomato tool
topaz torch tortoise towel tower tractor trail train trellis trout trumpet tulip tundra
tunnel turkey turnip turtle tuxedo twig umbrella unicorn urchin valley vanilla vase
velvet vessel vest village vine violet violin volcano voyage vulture wafer wagon walnut
walrus wand wave weasel whale wheat wheel whisker whistle willow window winter wizard
wolf wombat wood wool yacht yak yard yarn yogurt yolk zebra zenith zephyr zinc zipper
zone able agile ample ancient breezy bright brisk calm candid cheery chilly clever cozy
crisp curly dapper daring dusty eager early easy fancy fluffy frosty fuzzy gentle giant
glad golden grand happy hardy hasty hearty humble jolly jumpy keen kind lively lucky
mellow merry mighty misty modest neat nimble noble plucky polite proud quick quiet rapid
ready rustic shiny silent simple sleek sleepy smooth snappy snowy speedy spicy steady
sturdy sunny swift tidy tiny tranquil trusty velvety vivid warm wavy wise witty young
zesty amble bake bloom bounce build carve chase cheer climb dash dig drift fetch float
fold gather glide grow hike hop hum juggle jump knit laugh leap march mend nap plant
ponder race read roam row sing skate ski skip sleep slide sow spin splash sprint stack
stir stroll surf swim swing tinker toss travel trek twirl wander weave whirl write yodel
zoom aqua azure beige black bronze crimson cyan fuchsia gray green indigo khaki lavender
lilac magenta maroon mauve navy ochre pink purple red russet rust scarlet sepia tan teal
umber white yellow abacus acrobat adapter airship anthem antler aquarium archer atrium
avenue backpack ballet balloon bandana baton bayou beret biscuit blimp blizzard bluebird
bobsled bongo bookcase bowtie bracelet brownie bulldog burrito cabbage caboose calendar
camper caramel cardinal carousel cashew catapult catfish cello chalet chariot cheetah
chestnut chipmunk chowder cinnamon clarinet cobbler cockpit collar compost cookie cornet
cowbell crescent croquet crumpet cupboard cymbal dahlia dairy decimal dingo dinghy
dipper doorbell doughnut drizzle dumpling dynamo eggplant elevator emblem emu falafel
fiesta firefly flamingo flapjack fondue forklift freezer fritter galleon gazebo glitter
goblet goldfish gorilla granola gumdrop haiku hammock hazelnut hedgehog helium hibiscus
hilltop hotdog iceberg iguana inkwell jackal jukebox kangaroo ketchup keyboard kiln
kimono kitchen ladybug lasagna latitude lemonade limerick lollipop lullaby macaron
magnolia mailbox mammoth manatee mandolin marigold marmot mascot meerkat minnow molasses
mongoose mosaic muskrat nacho narwhal nebula nightowl oatmeal oboe ocelot omnibus orca
origami ostrich paddock pagoda pajamas panther paprika parsley parsnip peacock peapod
pegasus pheasant piccolo pinecone pinwheel platypus plumber poncho popsicle postcard
potluck quokka quinoa radiator raindrop ramen reindeer rhubarb rosemary sailboat sandbox
sardine sausage scallop scone seahorse sequoia sherbet skillet skylark smoothie sombrero
spatula spaniel starfish stingray strudel sunbeam sundae tamale thimble toboggan toucan
trombone tuba tugboat turbine ukulele unicycle waffle walkway warbler weekend wigwam
windmill yearbook zucchini
`
